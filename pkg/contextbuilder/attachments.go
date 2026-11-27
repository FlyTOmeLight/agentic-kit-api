package contextbuilder

import (
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/mediaurl"
)

const (
	MaxFileIndexEntries = 50
	maxFileIndexBytes   = 4 << 10
)

// IsDirectMediaAttachment selects URLs that can be sent as model media parts.
func IsDirectMediaAttachment(a Attachment, modalities []string) bool {
	if a.Kind != "image" && a.Kind != "video" {
		return false
	}
	if len(modalities) == 0 {
		if a.Kind != "image" {
			return false
		}
	} else if !slices.Contains(modalities, a.Kind) {
		return false
	}
	u, err := url.Parse(a.URI)
	return err == nil && strings.HasPrefix(a.URI, "https://") && u.User == nil &&
		!strings.Contains(a.URI, "#") && mediaurl.IsPublicHTTPS(u)
}

// FormatAttachmentsMetadata renders attachment references as text for the model.
func FormatAttachmentsMetadata(attachments []Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Attachments]\n")
	for _, a := range attachments {
		fmt.Fprintf(&b, "- %s %s (%s", a.Kind, strconv.Quote(a.Name), a.MediaType)
		if a.SizeBytes > 0 {
			fmt.Fprintf(&b, ", %d bytes", a.SizeBytes)
		}
		fmt.Fprintf(&b, ") %s", strconv.Quote(a.URI))
		if a.ArtifactURI != "" && a.ArtifactURI != a.URI {
			fmt.Fprintf(&b, " artifact=%s", strconv.Quote(a.ArtifactURI))
		}
		b.WriteByte('\n')
	}
	b.WriteString("[/Attachments]")
	return b.String()
}

// AttachmentSummaryText gives a text-only summary the same attachment facts.
func AttachmentSummaryText(attachments []Attachment) string {
	return FormatAttachmentsMetadata(attachments)
}

func projectAttachments(messages []Message, directFirst bool, modalities []string) {
	for i := range messages {
		m := &messages[i]
		var direct, metadata []Attachment
		for _, a := range m.Attachments {
			if directFirst && i == 0 && m.Role == RoleUser && IsDirectMediaAttachment(a, modalities) {
				direct = append(direct, a)
			} else {
				metadata = append(metadata, a)
			}
		}
		m.Attachments = direct
		if len(metadata) > 0 {
			if m.Content != "" {
				m.Content += "\n\n"
			}
			m.Content += FormatAttachmentsMetadata(metadata)
		}
	}
}

func attachmentFileIndex(attachments []Attachment) []FileIndexEntry {
	var entries []FileIndexEntry
	for _, a := range attachments {
		entries = append(entries, FileIndexEntry{
			URI: a.URI, Name: a.Name, MediaType: a.MediaType, SizeBytes: a.SizeBytes,
			Source: "attachment", Action: "uploaded",
		})
	}
	return entries
}

// RenderFileIndex renders newest-first lists, with live entries taking
// precedence over frozen entries for the same source and URI.
func RenderFileIndex(frozen, live []FileIndexEntry) (PinnedBlock, bool) {
	content := renderFileIndex(frozen, live)
	return PinnedBlock{Label: "files", Content: content}, content != ""
}

func renderFileIndex(frozen, live []FileIndexEntry) string {
	type key struct{ source, uri string }
	seen := make(map[key]bool, MaxFileIndexEntries)
	var b strings.Builder
	for _, group := range [][]FileIndexEntry{live, frozen} {
		for _, entry := range group {
			k := key{source: entry.Source, uri: entry.URI}
			if k.uri == "" || seen[k] {
				continue
			}
			label := "attachment"
			switch entry.Source {
			case "attachment":
			case "workspace":
				label = "workspace/historical"
				if entry.Accessible {
					label = "workspace/accessible"
				}
			default:
				continue
			}
			seen[k] = true
			name := entry.Name
			if name == "" {
				name = path.Base(entry.URI)
			}
			line := fmt.Sprintf(
				"[%s] %s %s %s",
				label,
				entry.Action,
				strconv.Quote(name),
				strconv.Quote(entry.URI),
			)
			if entry.MediaType != "" {
				line += " (" + entry.MediaType + ")"
			}
			if entry.SizeBytes > 0 {
				line += fmt.Sprintf(" %d bytes", entry.SizeBytes)
			}
			if b.Len() != 0 {
				if b.Len()+1+len(line) > maxFileIndexBytes {
					return b.String()
				}
				b.WriteByte('\n')
			} else if len(line) > maxFileIndexBytes {
				b.WriteString(utf8Prefix(line, maxFileIndexBytes))
				return b.String()
			}
			b.WriteString(line)
			if len(seen) == MaxFileIndexEntries {
				return b.String()
			}
		}
	}
	return b.String()
}

func utf8Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}
