package agenticv1_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	domain "github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestMemoryUpdateRestoreEncoding(t *testing.T) {
	for _, restore := range []bool{false, true} {
		name := "update"
		if restore {
			name = "restore"
		}
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/api/v1/memories/memory-1" ||
					r.Header.Get("Authorization") != "Bearer test-token" ||
					r.Header.Get("Idempotency-Key") != "memory-write-1" ||
					r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 3 {
					t.Errorf("body fields = %v", body)
				}
				var expectedRevision int64
				if err := json.Unmarshal(body["expectedRevision"], &expectedRevision); err != nil ||
					expectedRevision != 3 {
					t.Errorf("expectedRevision = %d, err = %v", expectedRevision, err)
				}
				var gotRestore bool
				if raw, ok := body["restore"]; !ok || json.Unmarshal(raw, &gotRestore) != nil ||
					gotRestore != restore {
					t.Errorf("restore = %s, want %t", body["restore"], restore)
				}
				var document domain.Document
				if err := json.Unmarshal(body["document"], &document); err != nil ||
					document.SubjectKey != "editor" || document.Summary != "editor preference" {
					t.Errorf("document = %+v, err = %v", document, err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"memory-1","revision":4}`)
			})

			entry, err := client.Memories.Update(t.Context(), "memory-1", &sdk.UpdateMemoryRequest{
				IdempotencyKey:   "memory-write-1",
				ExpectedRevision: 3,
				Restore:          restore,
				Document: sdk.MemoryDocument{
					Scope:      domain.Scope{Kind: domain.ScopeOwnerPrivate, Owner: "owner"},
					Kind:       domain.KindFact,
					SubjectKey: "editor",
					Summary:    "editor preference",
					Body:       json.RawMessage(`{"value":"spaces"}`),
					Source:     domain.Source{Class: domain.SourceHumanConfirmed},
				},
			})
			if err != nil || entry == nil || entry.ID != "memory-1" || entry.Revision != 4 {
				t.Fatalf("entry=%+v err=%v", entry, err)
			}
		})
	}
}

func TestMemoryCreateAndRetireEncoding(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/memories" ||
				r.Header.Get("Authorization") != "Bearer test-token" ||
				r.Header.Get("Idempotency-Key") != "memory-create-1" ||
				r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 {
				t.Errorf("body fields = %v", body)
			}
			var document domain.Document
			if err := json.Unmarshal(body["document"], &document); err != nil ||
				document.SubjectKey != "editor" || document.Summary != "editor preference" {
				t.Errorf("document = %+v, err = %v", document, err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"memory-1","revision":1}`)
		})

		entry, err := client.Memories.Create(t.Context(), &sdk.CreateMemoryRequest{
			IdempotencyKey: "memory-create-1",
			Document: sdk.MemoryDocument{
				Scope:      domain.Scope{Kind: domain.ScopeOwnerPrivate, Owner: "owner"},
				Kind:       domain.KindFact,
				SubjectKey: "editor",
				Summary:    "editor preference",
				Body:       json.RawMessage(`{"value":"spaces"}`),
				Source:     domain.Source{Class: domain.SourceHumanConfirmed},
			},
		})
		if err != nil || entry == nil || entry.ID != "memory-1" || entry.Revision != 1 {
			t.Fatalf("entry=%+v err=%v", entry, err)
		}
	})

	t.Run("retire", func(t *testing.T) {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/memories/memory-1/retire" ||
				r.Header.Get("Authorization") != "Bearer test-token" ||
				r.Header.Get("Idempotency-Key") != "memory-retire-1" ||
				r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 {
				t.Errorf("body fields = %v", body)
			}
			var expectedRevision int64
			if err := json.Unmarshal(body["expectedRevision"], &expectedRevision); err != nil ||
				expectedRevision != 3 {
				t.Errorf("expectedRevision = %d, err = %v", expectedRevision, err)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"memory-1","revision":4,"retiredAt":1}`)
		})

		entry, err := client.Memories.Retire(t.Context(), "memory-1", &sdk.RetireMemoryRequest{
			IdempotencyKey:   "memory-retire-1",
			ExpectedRevision: 3,
		})
		if err != nil || entry == nil || entry.ID != "memory-1" || entry.Revision != 4 ||
			entry.RetiredAt == nil {
			t.Fatalf("entry=%+v err=%v", entry, err)
		}
	})
}
