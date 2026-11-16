package service

import (
	"encoding/json"
	"net/http"
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestMemoryUpdateAndRestoreHTTP(t *testing.T) {
	store := integrationStore(t)
	router := NewRouter(t, store, nil, map[string]string{testToken: "owner"})

	assertError(
		t,
		request(router, http.MethodPost, "/v1/memories", memoryHTTPCreateBody("v1", "tabs"), testToken, ""),
		http.StatusBadRequest,
		"InvalidParameter",
	)
	assertError(
		t,
		request(
			router,
			http.MethodPost,
			"/v1/memories",
			memoryHTTPBodyWithIdempotencyKey(t, memoryHTTPCreateBody("v1", "tabs"), "body-key"),
			testToken,
			"memory-create-body-key",
		),
		http.StatusBadRequest,
		"InvalidParameter",
	)

	created := memoryHTTPCall(
		t,
		router,
		http.MethodPost,
		"/v1/memories",
		memoryHTTPCreateBody("v1", "tabs"),
		"memory-create-1",
	)
	if created.Revision != 1 || created.ID == "" || created.RetiredAt != nil {
		t.Fatalf("created = %+v", created)
	}

	assertError(
		t,
		request(
			router,
			http.MethodPut,
			"/v1/memories/"+created.ID,
			memoryHTTPUpdateBody(1, false, "v2", "spaces"),
			testToken,
			"",
		),
		http.StatusBadRequest,
		"InvalidParameter",
	)
	assertError(
		t,
		request(
			router,
			http.MethodPut,
			"/v1/memories/"+created.ID,
			memoryHTTPBodyWithIdempotencyKey(
				t,
				memoryHTTPUpdateBody(1, false, "v2", "spaces"),
				"body-key",
			),
			testToken,
			"memory-update-body-key",
		),
		http.StatusBadRequest,
		"InvalidParameter",
	)

	updated := memoryHTTPCall(
		t,
		router,
		http.MethodPut,
		"/v1/memories/"+created.ID,
		memoryHTTPUpdateBody(1, false, "v2", "spaces"),
		"memory-update-1",
	)
	if updated.Revision != 2 || updated.Summary != "v2" || updated.RetiredAt != nil {
		t.Fatalf("updated = %+v", updated)
	}

	assertError(
		t,
		request(
			router,
			http.MethodPost,
			"/v1/memories/"+created.ID+"/retire",
			`{"expectedRevision":2}`,
			testToken,
			"",
		),
		http.StatusBadRequest,
		"InvalidParameter",
	)
	assertError(
		t,
		request(
			router,
			http.MethodPost,
			"/v1/memories/"+created.ID+"/retire",
			memoryHTTPBodyWithIdempotencyKey(t, `{"expectedRevision":2}`, "body-key"),
			testToken,
			"memory-retire-body-key",
		),
		http.StatusBadRequest,
		"InvalidParameter",
	)

	retired := memoryHTTPCall(
		t,
		router,
		http.MethodPost,
		"/v1/memories/"+created.ID+"/retire",
		`{"expectedRevision":2}`,
		"memory-retire-1",
	)
	if retired.Revision != 3 || retired.RetiredAt == nil {
		t.Fatalf("retired = %+v", retired)
	}

	restoreBody := memoryHTTPUpdateBody(3, true, "v3", "spaces")
	restored := memoryHTTPCall(
		t,
		router,
		http.MethodPut,
		"/v1/memories/"+created.ID,
		restoreBody,
		"memory-restore-1",
	)
	if restored.Revision != 4 || restored.Summary != "v3" || restored.RetiredAt != nil {
		t.Fatalf("restored = %+v", restored)
	}

	replayed := memoryHTTPCall(
		t,
		router,
		http.MethodPut,
		"/v1/memories/"+created.ID,
		restoreBody,
		"memory-restore-1",
	)
	if replayed.Revision != restored.Revision || replayed.RetiredAt != nil {
		t.Fatalf("replayed = %+v, restored = %+v", replayed, restored)
	}
}

func memoryHTTPCall(
	t *testing.T,
	router http.Handler,
	method, path, body, key string,
) sdk.MemoryEntry {
	t.Helper()
	response := request(router, method, path, body, testToken, key)
	if response.Code != http.StatusOK && response.Code != http.StatusCreated {
		t.Fatalf("%s %s: status=%d body=%s", method, path, response.Code, response.Body.String())
	}
	var entry sdk.MemoryEntry
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func memoryHTTPCreateBody(summary, value string) string {
	return `{"document":` + memoryHTTPDocument(summary, value) + `}`
}

func memoryHTTPUpdateBody(revision int64, restore bool, summary, value string) string {
	body, err := json.Marshal(map[string]any{
		"expectedRevision": revision,
		"restore":          restore,
		"document":         json.RawMessage(memoryHTTPDocument(summary, value)),
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func memoryHTTPDocument(summary, value string) string {
	return `{
		"scope":{"kind":"OwnerPrivate","owner":"owner"},
		"kind":"Fact",
		"subjectKey":"editor",
		"summary":"` + summary + `",
		"body":{"value":"` + value + `"},
		"source":{"class":"HumanConfirmed"}
	}`
}

func memoryHTTPBodyWithIdempotencyKey(t *testing.T, body, key string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	rawKey, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	fields["idempotencyKey"] = rawKey
	rawBody, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(rawBody)
}
