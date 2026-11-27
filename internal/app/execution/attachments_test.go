package execution

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type attachmentStore struct {
	tenantArtifactStore
	files map[string]client.ResolvedAttachment
	calls int
	err   error
}

func (a *attachmentStore) ResolveAttachment(
	_ context.Context,
	_ client.Identity,
	uri string,
) (client.ResolvedAttachment, error) {
	a.calls++
	return a.files[uri], a.err
}

func TestArtifactAttachmentsAuthorizeAndReplay(t *testing.T) {
	const owner, project = "owner", "project"
	const alias = "artifact://tenant/media/aliases/latest/photo.jpg"
	const fixed = "artifact://tenant/media/versions/v1/photo.jpg"
	for _, chat := range []bool{true, false} {
		name := "application"
		if chat {
			name = "chat"
		}
		t.Run(name, func(t *testing.T) {
			store := tenantTestStore(t)
			scope := access.Scope{TenantID: "tenant", ProjectID: project}
			ctx := access.WithScope(t.Context(), scope)
			identity := client.Identity{
				TenantID:         scope.TenantID,
				ProjectID:        &scope.ProjectID,
				PropagatedUserID: owner,
			}
			config := agent.AgentConfig{
				Model:    agent.ModelSpec{Name: "test", BaseURL: "http://model.test"},
				MaxTurns: 5,
			}
			payload, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			remote := &attachmentStore{
				tenantArtifactStore: tenantArtifactStore{payload: payload},
				files: map[string]client.ResolvedAttachment{
					alias: {
						FileURI:   fixed,
						FileID:    "manifest-id",
						Path:      "files/photo.jpg",
						MediaType: "image/jpeg",
						SizeBytes: 512,
					},
				},
			}
			allowed := true
			checks := 0
			svc := NewService(store, remote).
				WithProjectReadCheck(func(_ context.Context, user string, got access.Scope) (bool, error) {
					checks++
					return allowed && user == owner && got == scope, nil
				})
			row := table.Agent{
				ID: durable.NewID(), TenantID: scope.TenantID, ProjectID: project, OwnerID: owner,
				Name: "agent", Draft: config, Revision: 1, PublishedRevision: 1, PublishedVersion: 1,
			}
			if err = store.DB().Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err = store.DB().Create(&table.AgentVersion{
				AgentID:            row.ID,
				Number:             1,
				ArtifactVersionID:  "version-1",
				ArtifactVersionURI: "artifact://tenant/agent/version-1",
			}).Error; err != nil {
				t.Fatal(err)
			}
			session, err := store.CreateSession(ctx, owner, row.ID, "attachments")
			if err != nil {
				t.Fatal(err)
			}
			query := "inspect the files"
			spec := execpkg.ExecutionSpec{
				AgentID:      row.ID,
				AgentVersion: 1,
				Query:        query,
				Scope: &workspace.Scope{
					TenantID:  scope.TenantID,
					ProjectID: project,
					ActorID:   owner,
					TaskID:    "task",
					RoundID:   "1",
				},
			}
			input := []execpkg.Attachment{
				{Kind: execpkg.AttachmentImage, ArtifactURI: alias},
				{
					Kind:      execpkg.AttachmentImage,
					URI:       "https://media.test/preview.jpg",
					SizeBytes: 42,
					Name:      "preview.jpg",
					MediaType: "image/jpeg",
				},
			}
			submit := func(ctx context.Context, identity client.Identity, key string, input []execpkg.Attachment) (*execpkg.Run, bool, error) {
				if chat {
					return svc.SubmitChat(
						ctx,
						identity,
						owner,
						session.ID,
						key,
						query,
						input,
						time.Minute,
						3,
						memory.SelectionOff,
						memory.RecallFilter{},
					)
				}
				spec.Attachments = input
				return svc.SubmitApplication(
					ctx,
					identity,
					owner,
					key,
					spec,
					time.Minute,
					memory.SelectionOff,
					memory.RecallFilter{},
				)
			}
			assertForbidden := func(ctx context.Context, identity client.Identity) {
				t.Helper()
				before := remote.calls
				if run, _, err := submit(
					ctx,
					identity,
					"key",
					input,
				); !errors.Is(err, memory.ErrForbidden) ||
					remote.calls != before {
					t.Fatalf(
						"denied submission = %+v %v, resolve calls=%d (was %d)",
						run,
						err,
						remote.calls,
						before,
					)
				}
			}
			assertForbidden(t.Context(), identity)
			allowed = false
			assertForbidden(ctx, identity)
			allowed = true
			otherProject := access.Scope{TenantID: scope.TenantID, ProjectID: "other-project"}
			otherIdentity := identity
			otherIdentity.ProjectID = &otherProject.ProjectID
			// Give the alternate project read permission so the binding check
			// itself must reject the session or application scope mismatch.
			svc.WithProjectReadCheck(
				func(context.Context, string, access.Scope) (bool, error) { return allowed, nil },
			)
			assertForbidden(access.WithScope(ctx, otherProject), otherIdentity)
			svc.WithProjectReadCheck(func(_ context.Context, user string, got access.Scope) (bool, error) {
				checks++
				return allowed && user == owner && got == scope, nil
			})
			otherIdentity = identity
			otherIdentity.PropagatedUserID = "other-owner"
			assertForbidden(ctx, otherIdentity)
			if !chat {
				spec.Scope.ActorID = "other-owner"
				assertForbidden(ctx, identity)
				spec.Scope.ActorID = owner
			}
			svc.artifact = nil
			if _, _, err = submit(ctx, identity, "key", input); !errors.Is(err, agent.ErrUnavailable) {
				t.Fatalf("missing artifact store: %v", err)
			}
			svc.artifact = remote
			first, created, err := submit(ctx, identity, "key", input)
			if err != nil || !created {
				t.Fatalf("submit = %+v %v %v", first, created, err)
			}
			want := []execpkg.Attachment{
				{
					ID:          "manifest-id",
					Kind:        execpkg.AttachmentImage,
					ArtifactURI: fixed,
					URI:         fixed,
					SizeBytes:   512,
					Name:        "photo.jpg",
					MediaType:   "image/jpeg",
				},
				{
					Kind:      execpkg.AttachmentImage,
					URI:       input[1].URI,
					SizeBytes: 42,
					Name:      "preview.jpg",
					MediaType: "image/jpeg",
				},
			}
			messages, err := store.Messages(ctx, owner, first.SessionID, 0, 10)
			if err != nil || !slices.Equal(first.Spec.Attachments, want) || len(messages) != 1 ||
				!slices.Equal(
					messages[0].Attachments,
					want,
				) || remote.calls != 1 || input[0].URI != "" {
				t.Fatalf(
					"frozen attachments = %+v, messages=%+v, calls=%d err=%v",
					first.Spec.Attachments,
					messages,
					remote.calls,
					err,
				)
			}
			moved := remote.files[alias]
			moved.FileURI = "artifact://tenant/media/versions/v2/photo.jpg"
			remote.files[alias] = moved
			for _, downstreamErr := range []error{nil, agent.ErrUnavailable} {
				remote.err = downstreamErr
				before := checks
				replay, created, err := submit(ctx, identity, "key", input)
				if err != nil || created || replay.ID != first.ID || remote.calls != 1 ||
					checks != before+1 || !slices.Equal(replay.Spec.Attachments, want) {
					t.Fatalf(
						"replay = %+v %v %v, resolve calls=%d project checks=%d",
						replay,
						created,
						err,
						remote.calls,
						checks,
					)
				}
			}
			allowed = false
			assertForbidden(ctx, identity)
			allowed = true
			assertForbidden(t.Context(), identity)
			reordered := []execpkg.Attachment{input[1], input[0]}
			if _, _, err = submit(
				ctx,
				identity,
				"key",
				reordered,
			); !errors.Is(err, durable.ErrConflict) ||
				remote.calls != 1 {
				t.Fatalf("different order = %v, resolve calls=%d", err, remote.calls)
			}
		})
	}
}

func TestAttachmentResolutionRevalidatesMetadata(t *testing.T) {
	const alias = "artifact://tenant/files/latest/photo.jpg"
	const textAlias = "artifact://tenant/files/latest/note.txt"
	resolved := client.ResolvedAttachment{
		FileURI: "artifact://tenant/files/version/photo.jpg", FileID: "file-1", Path: "photo.jpg",
		MediaType: "image/jpeg", SizeBytes: 50 << 20,
	}
	textResolved := client.ResolvedAttachment{
		FileURI: "artifact://tenant/files/version/note.txt", FileID: "file-2", Path: "",
		MediaType: "Text/Plain; charset=utf-8", SizeBytes: 128,
	}
	svc := NewService(nil, &attachmentStore{files: map[string]client.ResolvedAttachment{
		alias:     resolved,
		textAlias: textResolved,
	}})
	base := execpkg.Attachment{Kind: execpkg.AttachmentImage, ArtifactURI: alias}
	conflict := base
	conflict.MediaType = "image/png"
	mixed := base
	mixed.URI = "https://media.test/photo.jpg"
	for _, attachments := range [][]execpkg.Attachment{
		{conflict}, {base, base, base}, {mixed},
	} {
		if _, err := svc.resolveAttachments(
			t.Context(),
			client.Identity{},
			attachments,
		); !errors.Is(err, agent.ErrInvalid) &&
			!errors.Is(err, execpkg.ErrInvalid) {
			t.Fatalf("resolve accepted inconsistent metadata: %v", err)
		}
	}
	normalized, err := svc.resolveAttachments(
		t.Context(),
		client.Identity{},
		[]execpkg.Attachment{{Kind: execpkg.AttachmentFile, ArtifactURI: textAlias, MediaType: "text/plain"}},
	)
	if err != nil || len(normalized) != 1 || normalized[0].MediaType != "text/plain" ||
		normalized[0].Name != "file-2" {
		t.Fatalf("parameterized MIME normalization = %+v, err=%v", normalized, err)
	}
}
