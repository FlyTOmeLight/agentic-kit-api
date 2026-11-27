package execution

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func TestAttachmentValidation(t *testing.T) {
	image := Attachment{
		Kind:      AttachmentImage,
		Name:      "图片.jpg",
		MediaType: "image/jpeg",
		URI:       "https://media.test/p.jpg?download=1",
	}
	for _, tc := range []struct {
		name                string
		change              func(*Attachment)
		submitted, resolved bool
	}{
		{name: "clean absolute path", change: func(a *Attachment) { a.URI = "/workspace/p.jpg" }, submitted: true, resolved: true},
		{name: "native artifact", change: func(a *Attachment) { a.URI = "artifact://tenant/file/p.jpg"; a.ArtifactURI = a.URI }, submitted: true, resolved: true},
		{name: "byte maxima", change: func(a *Attachment) {
			a.ID = strings.Repeat("a", 128)
			a.Name = strings.Repeat("a", 255)
			a.URI += strings.Repeat("a", 2048-len(a.URI))
			a.SizeBytes = 50 << 20
		}, submitted: true, resolved: true},
		{name: "unresolved artifact", change: func(a *Attachment) {
			*a = Attachment{Kind: AttachmentImage, ArtifactURI: "artifact://tenant/file/p.jpg"}
		}, submitted: true},
		{name: "empty URI", change: func(a *Attachment) { a.URI = "" }},
		{name: "empty name", change: func(a *Attachment) { a.Name = " " }},
		{name: "long ID", change: func(a *Attachment) { a.ID = strings.Repeat("字", 43) }},
		{name: "control in ID", change: func(a *Attachment) { a.ID = "id\x7f" }},
		{name: "long name", change: func(a *Attachment) { a.Name = strings.Repeat("字", 86) }},
		{name: "control in name", change: func(a *Attachment) { a.Name = "a\r\nb" }},
		{name: "unsupported kind", change: func(a *Attachment) { a.Kind = "text" }},
		{name: "invalid MIME", change: func(a *Attachment) { a.MediaType = "image" }},
		{name: "uppercase MIME", change: func(a *Attachment) { a.MediaType = "Image/jpeg" }},
		{name: "MIME parameter", change: func(a *Attachment) { a.MediaType = "image/jpeg; quality=high" }},
		{name: "MIME wildcard", change: func(a *Attachment) { a.MediaType = "image/*" }},
		{name: "wrong MIME", change: func(a *Attachment) { a.Kind = AttachmentVideo }},
		{name: "document MIME", change: func(a *Attachment) { a.Kind = AttachmentDocument; a.MediaType = "application/pdf" }, submitted: true, resolved: true},
		{name: "audio MIME", change: func(a *Attachment) { a.Kind = AttachmentAudio; a.MediaType = "audio/mpeg" }, submitted: true, resolved: true},
		{name: "video MIME", change: func(a *Attachment) { a.Kind = AttachmentVideo; a.MediaType = "video/mp4" }, submitted: true, resolved: true},
		{name: "file MIME", change: func(a *Attachment) { a.Kind = AttachmentFile; a.MediaType = "text/plain" }, submitted: true, resolved: true},
		{name: "negative size", change: func(a *Attachment) { a.SizeBytes = -1 }},
		{name: "oversize", change: func(a *Attachment) { a.SizeBytes = 50<<20 + 1 }},
		{name: "long URI", change: func(a *Attachment) { a.URI += strings.Repeat("a", 2048) }},
		{name: "long ArtifactURI", change: func(a *Attachment) { a.ArtifactURI = "artifact://tenant/" + strings.Repeat("a", 2048) }},
		{name: "bad ArtifactURI scheme", change: func(a *Attachment) { a.ArtifactURI = a.URI }},
		{name: "ArtifactURI fragment", change: func(a *Attachment) { a.ArtifactURI = "artifact://tenant/x#fragment" }},
		{name: "ArtifactURI credentials", change: func(a *Attachment) { a.ArtifactURI = "artifact://user@tenant/x" }},
		{name: "artifact URI without ArtifactURI", change: func(a *Attachment) { a.URI = "artifact://tenant/file/p.jpg"; a.ArtifactURI = "" }},
		{name: "HTTP URI mixed with ArtifactURI", change: func(a *Attachment) {
			a.URI = "https://media.test/p.jpg"
			a.ArtifactURI = "artifact://tenant/file/p.jpg"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := image
			tc.change(&a)
			assertAttachmentValidation(t, []Attachment{a}, tc.submitted, tc.resolved)
		})
	}
	for _, uri := range []string{
		"file:///workspace/p", "javascript:alert(1)", "data:image/png;base64,aaaa", "relative/path",
		"//address/file", "/root/../p", "/root/./p", "/root//p", " https://media.test/p",
		"https://media.test/p ", "https://media.test/p\tq", "https:///p", "https://:443/p",
		"https://u:p@media.test/p", "https://media.test/p#", "artifact://tenant",
	} {
		t.Run(uri, func(t *testing.T) {
			a := image
			a.URI = uri
			assertAttachmentValidation(t, []Attachment{a}, false, false)
		})
	}
	assertAttachmentValidation(t, nil, true, true)
	assertAttachmentValidation(t, slices.Repeat([]Attachment{image}, 10), true, true)
	assertAttachmentValidation(t, slices.Repeat([]Attachment{image}, 11), false, false)
	big := image
	big.SizeBytes = 50 << 20
	assertAttachmentValidation(t, []Attachment{big, big}, true, true)
	image.SizeBytes = 1
	assertAttachmentValidation(t, []Attachment{big, big, image}, false, false)
}

func assertAttachmentValidation(t *testing.T, attachments []Attachment, submitted, resolved bool) {
	t.Helper()
	for _, check := range []struct {
		validate func([]Attachment) error
		valid    bool
	}{{validate: ValidateSubmittedAttachments, valid: submitted}, {validate: ValidateAttachments, valid: resolved}} {
		err := check.validate(attachments)
		if (check.valid && err != nil) || (!check.valid && !errors.Is(err, ErrInvalid)) {
			t.Fatalf("valid=%v, got %v", check.valid, err)
		}
	}
}

func TestCompileRunSpecClonesAttachmentAndModelInputs(t *testing.T) {
	modalities := []string{"image"}
	attachments := []Attachment{{Name: "original"}}
	config := agent.AgentConfig{Model: agent.ModelSpec{InputModalities: modalities}}
	spec := CompileRunSpec(config, "agent", 1, "query", nil, nil, attachments...)
	sub := CloneSubAgents([]SubAgentSnapshot{{Model: config.Model}})
	modalities[0] = "video"
	attachments[0].Name = "changed"
	if spec.Attachments[0].Name != "original" || spec.Model.InputModalities[0] != "image" ||
		sub[0].Model.InputModalities[0] != "image" {
		t.Fatalf("snapshot retained mutable inputs: %+v, %+v", spec, sub)
	}
}
