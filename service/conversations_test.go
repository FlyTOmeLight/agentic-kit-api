package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func TestAgentEditingAndPersistentSessions(t *testing.T) {
	store := integrationStore(t)
	tokens := map[string]string{testToken: "owner", "other-token-with-24-characters": "other"}
	router := NewRouter(t, store, nil, tokens)
	session := createHTTPConversation(t, router)
	agentPath := "/v1/agents/" + session.AgentID
	sessionPath := "/v1/sessions/" + session.ID
	var a durable.Agent
	w := request(router, "GET", agentPath, "", testToken, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	a.Draft.SystemPrompt = "new prompt"
	a.Draft.Tools = []durable.ToolSpec{
		{
			Name:           "deliver",
			URL:            "http://tools/deliver",
			Policy:         durable.Unsafe,
			ReturnDirectly: true,
			Parameters:     json.RawMessage(`{"type":"object"}`),
		},
	}
	body, _ := json.Marshal(map[string]any{"name": "updated", "revision": a.Revision, "config": a.Draft})
	w = request(router, "PUT", agentPath, string(body), testToken, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertError(t, request(router, "PUT", agentPath, string(body), testToken, ""), 409, "Conflict")
	w = request(router, "POST", agentPath+"/publish", `{"revision":2}`, testToken, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var v durable.AgentVersion
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Number != 2 || v.Config.SystemPrompt != "new prompt" || len(v.Config.Tools) != 1 ||
		!v.Config.Tools[0].ReturnDirectly {
		t.Fatalf("version=%+v", v)
	}
	// Recreating the router must load definitions and history from the database.
	router = NewRouter(t, store, nil, tokens)
	w = request(router, "POST", sessionPath+"/messages", `{"query":"hello"}`, testToken, "request")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var run durable.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.AgentID != session.AgentID || run.ConfigVersion != "2" {
		t.Fatalf("run=%+v", run)
	}
	frozen, err := store.Get(t.Context(), "owner", run.ID)
	if err != nil || len(frozen.Spec.Tools) != 1 || !frozen.Spec.Tools[0].ReturnDirectly {
		t.Fatalf("returnDirectly was not frozen in the run: %v", err)
	}
	for _, path := range []string{"/v1/agents", "/v1/sessions?agentID=" + session.AgentID, sessionPath + "/runs", sessionPath + "/messages"} {
		w = request(router, "GET", path, "", testToken, "")
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(page.Items) != 1 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = request(router, "GET", sessionPath+"/messages?after=1", "", testToken, "")
	if w.Code != 200 || w.Body.String() != `{"items":[]}` {
		t.Fatalf("cursor %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{agentPath, agentPath + "/versions/1", sessionPath, sessionPath + "/messages", sessionPath + "/runs"} {
		assertError(t, request(router, "GET", path, "", "other-token-with-24-characters", ""), 404, "NotFound")
	}
	assertError(
		t,
		request(
			router,
			"POST",
			sessionPath+"/messages",
			`{"query":"hello"}`,
			"other-token-with-24-characters",
			"request",
		),
		404,
		"NotFound",
	)
	assertError(
		t,
		request(router, "PUT", agentPath, string(body), "other-token-with-24-characters", ""),
		404,
		"NotFound",
	)
	assertError(
		t,
		request(
			router,
			"POST",
			"/v1/sessions",
			fmt.Sprintf(`{"agentID":%q}`, session.AgentID),
			"other-token-with-24-characters",
			"",
		),
		404,
		"NotFound",
	)
}

func TestConfigurationInputValidation(t *testing.T) {
	store := integrationStore(t)
	router := NewRouter(
		t,
		store,
		nil,
		map[string]string{testToken: "owner", "reader-token-with-24-characters": "reader"},
	)
	for _, tc := range []struct{ method, path, body, code string }{
		{method: "POST", path: "/v1/agents", body: `{"name":"agent","config":{"maxTurns":5}}`, code: "InvalidParameter"},
		{method: "POST", path: "/v1/agents", body: `{"name":"agent","config":{"model":{"name":"test","baseURL":"file:///etc/passwd"},"maxTurns":5}}`, code: "InvalidParameter"},
		{method: "POST", path: "/v1/sessions/s/messages", body: `{"query":"hello","agentID":"other"}`, code: "InvalidParameter"},
		{method: "GET", path: "/v1/agents?limit=0", body: "", code: "InvalidParameter"},
		{method: "GET", path: "/v1/sessions?limit=101", body: "", code: "InvalidParameter"},
		{method: "GET", path: "/v1/sessions/s/messages?after=-1", body: "", code: "InvalidParameter"},
		{method: "GET", path: "/v1/agents/a/versions/bad", body: "", code: "InvalidParameter"},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			assertError(t, request(router, tc.method, tc.path, tc.body, testToken, "key"), 400, tc.code)
		})
	}
	assertError(
		t,
		request(router, "POST", "/v1/sessions/missing/messages", `{"query":"hello"}`, testToken, "key"),
		404,
		"NotFound",
	)
}
