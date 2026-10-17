package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
)

func TestTenantUsesPublishedSDK(t *testing.T) {
	token := strings.Repeat("t", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("invalid SDK request")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/v1/session/check":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["session"] != "cookie" {
				t.Error("lost session input", err)
			}
			_, _ = w.Write([]byte(`{"id":"user"}`))
		case "/internal/v1/access/check":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"DENIED","message":"denied"}}`))
		case "/internal/v1/workspace/check":
			_, _ = w.Write(
				[]byte(
					`{"allowed":true,"canEdit":true,"projects":[{"id":"project","name":"Project","level":30}]}`,
				),
			)
		}
	}))
	defer server.Close()
	tenant, err := NewTenant(
		framesdk.Config{
			Endpoint:    server.URL,
			Credentials: &framesdk.Credentials{AuthType: framesdk.AuthTypeAPIKey, APIKey: token},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer tenant.Close()
	var session struct {
		ID string `json:"id"`
	}
	if status := tenant.Check(
		context.Background(),
		"/internal/v1/session/check",
		map[string]string{"session": "cookie"},
		&session,
	); status != 200 ||
		session.ID != "user" {
		t.Fatal(status, session)
	}
	var result any
	if status := tenant.Check(
		context.Background(),
		"/internal/v1/access/check",
		map[string]string{},
		&result,
	); status != 403 {
		t.Fatal(status)
	}
	var workspace struct {
		Allowed bool `json:"allowed"`
		CanEdit bool `json:"canEdit"`
	}
	if status := tenant.Check(
		context.Background(),
		"/internal/v1/workspace/check",
		map[string]string{},
		&workspace,
	); status != 200 || !workspace.Allowed ||
		!workspace.CanEdit {
		t.Fatal(status, workspace)
	}
}

func TestForgeConfigRejectsUnsupportedSettings(t *testing.T) {
	for _, config := range []framesdk.Config{
		{Endpoint: "http://example.com", Retry: 1},
		{Endpoint: "http://example.com", Timeout: -1},
		{Endpoint: "http://example.com", Credentials: &framesdk.Credentials{AuthType: framesdk.AuthTypeAKSK}},
	} {
		if _, err := NewTenant(config); err == nil {
			t.Error("Tenant accepted unsupported configuration")
		}
		if _, err := NewCodingEnvironment(config); err == nil {
			t.Error("Coding accepted unsupported configuration")
		}
	}
	if _, err := NewTenant(framesdk.Config{Endpoint: "http://example.com", ProxyURL: "http://proxy"}); err == nil {
		t.Error("Tenant accepted unsupported proxy configuration")
	}
}
