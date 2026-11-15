package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestFilesystemBindingIsFrozenAndRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	config := AgentConfig{
		Model:    ModelSpec{Name: "model", BaseURL: "http://model.test/v1"},
		MaxTurns: 3,
		Toolsets: []string{"filesystem"},
	}
	a := createAgentFixture(t, s, "owner", "Coding AIO", config)
	v := publishAgentFixture(t, s, "owner", a.ID, a.Revision)
	spec := sdk.ExecutionSpec{
		Scope: &sdk.Scope{
			TenantID:  "tenant",
			ProjectID: "project",
			ActorID:   "actor",
			TaskID:    "task",
			RoundID:   "round",
		},
		AgentID:      a.ID,
		AgentVersion: v.Number,
		Query:        "fix",
	}
	if _, _, err := submitExecutionFixture(
		t,
		s,
		"owner",
		"round",
		spec,
		time.Minute,
		config,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("missing workspace accepted: %v", err)
	}
	spec.Filesystem = &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://aio.test",
		TokenEnv:  "AIO_TOKEN",
		Workspace: "/workspace/repo",
	}
	r, created, err := submitExecutionFixture(t, s, "owner", "round", spec, time.Minute, config)
	if err != nil || !created {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Filesystem == nil || got.Spec.Filesystem.Workspace != "/workspace/repo" ||
		len(got.Spec.Toolsets) != 1 {
		t.Fatalf("binding lost: %+v", got.Spec)
	}
	spec.Filesystem.Workspace = "/workspace/different"
	if _, _, err = submitExecutionFixture(
		t,
		s,
		"owner",
		"round",
		spec,
		time.Minute,
		config,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("binding changed on replay: %v", err)
	}
}
