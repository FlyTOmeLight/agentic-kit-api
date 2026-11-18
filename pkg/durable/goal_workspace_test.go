package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func TestGoalIterationRequiresSavedWorkspace(t *testing.T) {
	for _, scenario := range []string{
		"saved", "saving", "released without checkpoint", "old epoch", "wrong profile", "lost", "ambiguous", "outside workspace",
	} {
		t.Run(scenario, func(t *testing.T) {
			store := testStore(t)
			goal, previous := failedGoalIteration(t, store)
			ctx := context.Background()
			current, err := store.Get(ctx, "owner", previous.ID)
			if err != nil {
				t.Fatal(err)
			}
			spec := current.Spec
			spec.Filesystem = &FilesystemBinding{
				Provider: "Sandbox", URL: "http://sandbox-center", TokenEnv: "SANDBOX_TOKEN",
				Workspace: "/workspace/repo", RestoreFromSandboxID: "older-sandbox",
			}
			if scenario == "outside workspace" {
				spec.Filesystem.Workspace = "/tmp/repo"
			}
			data, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.db.Model(&table.Run{}).
				Where("id = ?", previous.ID).
				Update("spec", string(data)).
				Error; err != nil {
				t.Fatal(err)
			}
			env := table.RunEnvironment{
				ID:              NewID(),
				RunID:           previous.ID,
				RequestKey:      NewID(),
				RunEpoch:        current.Epoch,
				ProfileID:       "coding",
				ProfileRevision: 1,
				SandboxID:       "saved-sandbox",
				LeaseID:         "saved-lease",
				Generation:      1,
				Status:          EnvironmentReleased,
				SavedCheckpoint: fmt.Sprintf("sha256:%064x:%064x", 1, 2),
			}
			switch scenario {
			case "saving":
				env.Status = EnvironmentActive
				env.SavedCheckpoint = ""
			case "released without checkpoint":
				env.SavedCheckpoint = ""
			case "old epoch":
				env.RunEpoch--
			case "wrong profile":
				env.ProfileID = "different"
			case "lost":
				env.Status = EnvironmentLost
			}
			if err = store.db.Create(&env).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "ambiguous" {
				other := env
				other.ID, other.RequestKey, other.SandboxID = NewID(), NewID(), "another-sandbox"
				if err = store.db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			next, err := store.QueueGoalIteration(ctx)
			if scenario == "saving" {
				if !errors.Is(err, ErrNoWork) {
					t.Fatalf("saving workspace was eligible: next=%+v err=%v", next, err)
				}
				state, readErr := store.GetGoal(ctx, "owner", goal.ID)
				if readErr != nil || state.Status != GoalActive {
					t.Fatalf("saving goal did not remain active: %+v %v", state, readErr)
				}
				if err = store.db.Model(&table.RunEnvironment{}).Where("id = ?", env.ID).
					Updates(map[string]any{"status": EnvironmentReleased, "saved_checkpoint": fmt.Sprintf("sha256:%064x:%064x", 1, 2)}).
					Error; err != nil {
					t.Fatal(err)
				}
				next, err = store.QueueGoalIteration(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "saved" || scenario == "saving" {
				if next == nil || next.Spec.Filesystem == nil ||
					next.Spec.Filesystem.RestoreFromSandboxID != env.SandboxID ||
					next.Spec.Filesystem.Workspace != spec.Filesystem.Workspace ||
					next.GoalID != goal.ID {
					t.Fatalf("continuation lost workspace identity: %+v", next)
				}
				original, readErr := store.Get(ctx, "owner", previous.ID)
				if readErr != nil || original.Spec.Filesystem.RestoreFromSandboxID != "older-sandbox" {
					t.Fatalf("previous snapshot changed: %+v %v", original, readErr)
				}
				if _, err = store.QueueGoalIteration(ctx); !errors.Is(err, ErrNoWork) {
					t.Fatalf("workspace continuation duplicated: %v", err)
				}
				return
			}
			state, readErr := store.GetGoal(ctx, "owner", goal.ID)
			if next != nil || readErr != nil || state.Status != GoalBlocked ||
				state.ActiveRunID != previous.ID {
				t.Fatalf(
					"unrecoverable workspace did not block: next=%+v goal=%+v err=%v",
					next,
					state,
					readErr,
				)
			}
		})
	}
}
