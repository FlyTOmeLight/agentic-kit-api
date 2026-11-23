package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/constants"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestMemoryActorProjectMaintenanceHTTP(t *testing.T) {
	store := integrationStore(t)
	authentication := newTestAuthentication(map[string]string{testToken: "owner"})
	allowed := true
	readAllowed := true
	authentication.ProjectRead = func(_ context.Context, _ string, _ access.Scope) (bool, error) {
		return readAllowed, nil
	}
	authentication.ProjectWrite = access.ProjectWriteCheck(
		func(_ context.Context, _ string, scope access.Scope) (bool, error) {
			return allowed && scope.ProjectID != "", nil
		},
	)
	router := newRouterWithAuthentication(t, store, nil, authentication, config.CallbackConfig{}, nil)

	projectRequest := func(
		project, method, path, body, key string,
	) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+testToken)
		if project != "" {
			r.Header.Set(constants.HeaderProjectID, project)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}

	assertError(t, projectRequest(
		"", http.MethodPost, "/v1/memories", actorProjectBody("v1"), "project-missing-scope",
	), http.StatusForbidden, "Forbidden")

	created := decodeEntry(t, projectRequest(
		"project-a", http.MethodPost, "/v1/memories", actorProjectBody("v1"), "project-create",
	), http.StatusCreated)
	if created.Scope.Kind != memory.ScopeActorProject ||
		created.Scope.Project != "project-a" ||
		created.OutputVisibility != memory.OutputProject ||
		created.Revision != 1 {
		t.Fatalf("created = %+v", created)
	}

	assertError(t, projectRequest(
		"project-b", http.MethodPut, "/v1/memories/"+created.ID,
		actorProjectUpdateBody(created.Revision, false, "project-b", "v2"), "project-update-cross",
	), http.StatusNotFound, "NotFound")

	updated := decodeEntry(t, projectRequest(
		"project-a", http.MethodPut, "/v1/memories/"+created.ID,
		actorProjectUpdateBody(created.Revision, false, "project-a", "v2"), "project-update",
	), http.StatusOK)
	if updated.Revision != 2 || updated.Summary != "v2" {
		t.Fatalf("updated = %+v", updated)
	}

	retired := decodeEntry(t, projectRequest(
		"project-a", http.MethodPost, "/v1/memories/"+created.ID+"/retire",
		fmt.Sprintf(`{"expectedRevision":%d}`, updated.Revision), "project-retire",
	), http.StatusOK)
	if retired.RetiredAt == nil {
		t.Fatalf("retired = %+v", retired)
	}

	restored := decodeEntry(t, projectRequest(
		"project-a", http.MethodPut, "/v1/memories/"+created.ID,
		actorProjectUpdateBody(retired.Revision, true, "project-a", "v3"), "project-restore",
	), http.StatusOK)
	if restored.Revision != 4 || restored.RetiredAt != nil {
		t.Fatalf("restored = %+v", restored)
	}

	readAllowed = false
	assertError(t, projectRequest(
		"project-a", http.MethodGet, "/v1/memories/"+created.ID, "", "",
	), http.StatusNotFound, "NotFound")
	assertError(t, projectRequest(
		"project-a", http.MethodGet, "/v1/memories", "", "",
	), http.StatusForbidden, "Forbidden")

	allowed = false
	assertError(t, projectRequest(
		"project-a", http.MethodPost, "/v1/memories", actorProjectBody("v1"), "project-create",
	), http.StatusForbidden, "Forbidden")
	assertError(t, projectRequest(
		"project-a", http.MethodPut, "/v1/memories/"+created.ID,
		actorProjectUpdateBody(created.Revision, false, "project-a", "v2"), "project-update",
	), http.StatusForbidden, "Forbidden")
	assertError(t, projectRequest(
		"project-a", http.MethodPost, "/v1/memories/"+created.ID+"/retire",
		fmt.Sprintf(`{"expectedRevision":%d}`, updated.Revision), "project-retire",
	), http.StatusForbidden, "Forbidden")
	assertError(t, projectRequest(
		"project-a", http.MethodPut, "/v1/memories/"+created.ID,
		actorProjectUpdateBody(retired.Revision, true, "project-a", "v3"), "project-restore",
	), http.StatusForbidden, "Forbidden")
}

func actorProjectBody(summary string) string {
	return actorProjectBodyForProject("project-a", summary)
}

func actorProjectBodyForProject(project, summary string) string {
	body, err := json.Marshal(map[string]any{
		"document": map[string]any{
			"scope": map[string]string{
				"kind":    "ActorProject",
				"tenant":  "tenant",
				"project": project,
			},
			"kind":             "Fact",
			"subjectKey":       "editor",
			"summary":          summary,
			"body":             json.RawMessage(`{"value":"` + summary + `"}`),
			"outputVisibility": "Project",
			"source":           map[string]string{"class": "HumanConfirmed"},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func actorProjectUpdateBody(revision int64, restore bool, project, summary string) string {
	document := json.RawMessage(actorProjectBodyForProject(project, summary))
	var fields struct {
		Document json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(document, &fields); err != nil {
		panic(err)
	}
	body, err := json.Marshal(map[string]any{
		"expectedRevision": revision,
		"restore":          restore,
		"document":         fields.Document,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func decodeEntry(
	t *testing.T, response *httptest.ResponseRecorder, wantStatus int,
) sdk.MemoryEntry {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", response.Code, wantStatus, response.Body.String())
	}
	var entry sdk.MemoryEntry
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}
