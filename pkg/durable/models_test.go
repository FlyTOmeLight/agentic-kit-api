package durable

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// Bypass Store validation so these tests exercise the database constraints.
func TestModelConstraints(t *testing.T) {
	s := testStore(t)
	run := createRun(t, s)
	step := table.Step{RunID: run.ID, StepID: "step", InputHash: "hash", Policy: ReadOnly, Status: "Started"}
	if err := s.db.Create(&step).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		write func() error
		want  error
	}{
		{name: "run status", write: func() error {
			return s.db.Model(&table.Run{}).Where("id = ?", run.ID).Update("status", "invalid").Error
		}, want: gorm.ErrCheckConstraintViolated},
		{name: "step policy", write: func() error {
			return s.db.Model(&table.Step{}).Where("run_id = ?", run.ID).Update("policy", "invalid").Error
		}, want: gorm.ErrCheckConstraintViolated},
		{name: "step status", write: func() error {
			return s.db.Model(&table.Step{}).Where("run_id = ?", run.ID).Update("status", "invalid").Error
		}, want: gorm.ErrCheckConstraintViolated},
		{name: "step foreign key", write: func() error {
			return s.db.Create(
				&table.Step{
					RunID:     "missing",
					StepID:    "step",
					InputHash: "hash",
					Policy:    ReadOnly,
					Status:    "Started",
				},
			).Error
		}, want: gorm.ErrForeignKeyViolated},
		{name: "event foreign key", write: func() error {
			return s.db.Create(
				&table.Event{
					RunID:   "missing",
					Seq:     1,
					Kind:    "Progress",
					Payload: json.RawMessage(`{}`),
				},
			).Error
		}, want: gorm.ErrForeignKeyViolated},
		{
			name:  "step primary key",
			write: func() error { return s.db.Create(&step).Error },
			want:  gorm.ErrDuplicatedKey,
		},
		{name: "event primary key", write: func() error {
			return s.db.Create(
				&table.Event{RunID: run.ID, Seq: 1, Kind: "Progress", Payload: json.RawMessage(`{}`)},
			).Error
		}, want: gorm.ErrDuplicatedKey},
		{
			name:  "referenced run deletion",
			write: func() error { return s.db.Delete(&table.Run{}, "id = ?", run.ID).Error },
			want:  gorm.ErrForeignKeyViolated,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestModelSessionAndRequestUniqueness(t *testing.T) {
	s := testStore(t)
	for _, status := range []Status{Queued, Running, WaitingClarification, NeedsReview, Completed, Failed, Canceled} {
		t.Run(string(status), func(t *testing.T) {
			tx := s.db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			first := table.Run{
				ID:          NewID(),
				OwnerID:     "owner",
				SessionID:   "session",
				RequestKey:  "first",
				RequestHash: "hash",
				Spec:        table.ExecutionSnapshot{},
				Status:      status,
				Deadline:    time.Now().Add(time.Hour),
				MaxAttempts: 3,
			}
			if err := tx.Create(&first).Error; err != nil {
				t.Fatal(err)
			}
			second := first
			second.ID, second.RequestKey = NewID(), "second"
			err := tx.Create(&second).Error
			if status.Terminal() {
				if err != nil {
					t.Fatal(err)
				}
				// Request keys remain unique even across different sessions and after completion.
				second.ID, second.SessionID = NewID(), "another-session"
				if err = tx.Create(&second).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
					t.Fatalf("duplicate request accepted: %v", err)
				}
			} else if !errors.Is(err, gorm.ErrDuplicatedKey) {
				t.Fatalf("concurrent active session accepted: %v", err)
			}
		})
	}
}

func TestModelDatabaseDefaults(t *testing.T) {
	s := testStore(t)
	id := NewID()
	// A map leaves omitted fields to PostgreSQL instead of GORM's struct defaults.
	err := s.db.Model(&table.Run{}).Create(map[string]any{
		"id":           id,
		"owner_id":     "owner",
		"session_id":   "session",
		"request_key":  "request",
		"request_hash": "hash",
		"spec":         "{}",
		"status":       Queued,
		"deadline":     gorm.Expr("clock_timestamp()+interval '1 hour'"),
		"max_attempts": 3,
	}).Error
	if err != nil {
		t.Fatal(err)
	}
	var got table.Run
	if err = s.db.Take(&got, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if string(got.State) != "{}" || got.LeaseUntil != nil || got.Epoch != 0 || got.Attempt != 0 ||
		got.EventSeq != 0 ||
		got.Output != "" ||
		got.Error != "" ||
		got.WorkerID != "" {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.AvailableAt.IsZero() ||
		!got.Deadline.After(got.CreatedAt) {
		t.Fatalf("missing database timestamps: %+v", got)
	}
}
