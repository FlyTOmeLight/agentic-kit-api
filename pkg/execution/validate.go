package execution

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

// Interaction reports the pause kind this boundary carries. It is empty for
// boundaries that do not wait for a human.
func (k BoundaryKind) Interaction() InteractionKind {
	switch k {
	case BoundaryAwaitClarification:
		return InteractionClarification
	case BoundaryAwaitReview:
		return InteractionReview
	default:
		return ""
	}
}

// Validate rejects any field combination the store cannot commit. It runs
// before a transaction is opened, so an invalid boundary writes nothing.
func (b Boundary) Validate() error {
	if len(b.State) == 0 || !json.Valid(b.State) {
		return ErrInvalid
	}
	if b.Turn != nil && (b.Kind != BoundaryModelStarted || *b.Turn < 0) {
		return ErrInvalid
	}
	switch b.Kind {
	case BoundaryModelStarted:
		if b.Output != "" || b.Detail != "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryComplete:
		if b.Detail != "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryFail:
		if b.Output != "" || b.Detail == "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryBlock:
		if b.Output != "" || b.Detail == "" || b.WaitingStep != "" {
			return ErrInvalid
		}
	case BoundaryAwaitClarification, BoundaryAwaitReview:
		if b.Output != "" || b.Detail == "" || b.WaitingStep == "" {
			return ErrInvalid
		}
	case BoundaryAwaitExternal:
		if b.Output != "" || b.Detail == "" || b.WaitingStep == "" || b.ExternalRef == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Validate rejects an intent the store cannot record.
func (i StepIntent) Validate() error {
	if i.ID == "" {
		return ErrInvalid
	}
	switch i.Policy {
	case agent.ReadOnly, agent.Idempotent, agent.Unsafe:
		return nil
	default:
		return ErrInvalid
	}
}

// ValidateSubmittedAttachments accepts metadata that can be filled in by
// resolving an artifact file during submission.
func ValidateSubmittedAttachments(attachments []Attachment) error {
	return validateAttachments(attachments, true)
}

// ValidateAttachments validates the fully resolved values before persistence.
func ValidateAttachments(attachments []Attachment) error {
	return validateAttachments(attachments, false)
}

func validateAttachments(attachments []Attachment, allowUnresolved bool) error {
	if len(attachments) > 10 {
		return fmt.Errorf("%w: at most 10 attachments", ErrInvalid)
	}
	var total int64
	for i, a := range attachments {
		if err := validateAttachment(a, allowUnresolved); err != nil {
			return fmt.Errorf("attachment %d: %w", i+1, err)
		}
		total += a.SizeBytes
	}
	if total > 100<<20 {
		return fmt.Errorf("%w: attachments exceed 100 MiB", ErrInvalid)
	}
	return nil
}

func validateAttachment(a Attachment, allowUnresolved bool) error {
	if !validAttachmentText(a.ID, 128) {
		return fmt.Errorf("%w: attachment ID", ErrInvalid)
	}
	unresolved := allowUnresolved && a.ArtifactURI != ""
	if !validAttachmentText(a.Name, 255) || (strings.TrimSpace(a.Name) == "" && (!unresolved || a.Name != "")) {
		return fmt.Errorf("%w: attachment name", ErrInvalid)
	}
	switch a.Kind {
	case AttachmentImage, AttachmentDocument, AttachmentAudio, AttachmentVideo, AttachmentFile:
	default:
		return fmt.Errorf("%w: attachment kind", ErrInvalid)
	}
	if !(unresolved && a.MediaType == "") {
		if !validMediaType(a.MediaType) {
			return fmt.Errorf("%w: attachment media type", ErrInvalid)
		}
		switch a.Kind {
		case AttachmentImage, AttachmentAudio, AttachmentVideo:
			if !strings.HasPrefix(a.MediaType, string(a.Kind)+"/") {
				return fmt.Errorf("%w: attachment media type does not match kind", ErrInvalid)
			}
		}
	}
	if a.SizeBytes < 0 || a.SizeBytes > 50<<20 {
		return fmt.Errorf("%w: attachment size must be between 0 and 50 MiB", ErrInvalid)
	}
	if !(unresolved && a.URI == "") && !validAttachmentURI(a.URI, false) {
		return fmt.Errorf("%w: attachment URI", ErrInvalid)
	}
	if a.ArtifactURI != "" && !validAttachmentURI(a.ArtifactURI, true) {
		return fmt.Errorf("%w: attachment artifact URI", ErrInvalid)
	}
	if strings.HasPrefix(a.URI, "artifact://") && a.ArtifactURI == "" {
		return fmt.Errorf("%w: artifact URI requires artifactURI", ErrInvalid)
	}
	if a.ArtifactURI != "" && a.URI != "" && !strings.HasPrefix(a.URI, "artifact://") {
		return fmt.Errorf("%w: artifactURI cannot be combined with external URI", ErrInvalid)
	}
	return nil
}

func validAttachmentText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) == -1
}

func validMediaType(s string) bool {
	parsed, params, err := mime.ParseMediaType(s)
	return err == nil && len(params) == 0 && parsed == s && strings.Contains(s, "/") && !strings.Contains(s, "*")
}

func validAttachmentURI(raw string, artifactOnly bool) bool {
	if raw == "" || !validAttachmentText(raw, 2048) || raw != strings.TrimSpace(raw) {
		return false
	}
	if !artifactOnly && strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//") && !strings.Contains(raw, "..") && path.Clean(raw) == raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#") {
		return false
	}
	switch {
	case strings.HasPrefix(raw, "artifact://"):
		return u.Path != ""
	case !artifactOnly && (strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")):
		return true
	default:
		return false
	}
}

// ValidateFileArtifacts checks the workspace metadata stored with a tool result.
func ValidateFileArtifacts(artifacts []FileArtifact) error {
	for i, a := range artifacts {
		if a.Path == "" || a.Path != strings.TrimSpace(a.Path) || !validAttachmentText(a.Path, 2048) ||
			path.Clean(a.Path) != a.Path || a.Path == "." || a.Path == ".." ||
			strings.HasPrefix(a.Path, "../") ||
			!validAttachmentText(a.Name, 255) || (a.MediaType != "" && !validMediaType(a.MediaType)) ||
			a.SizeBytes < 0 || (a.Action != "written" && a.Action != "edited") ||
			(a.Provider != "" && a.Provider != "AIO" && a.Provider != "Sandbox") {
			return fmt.Errorf("%w: file artifact %d", ErrInvalid, i+1)
		}
	}
	return nil
}
