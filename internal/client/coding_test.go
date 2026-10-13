package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestCodingEnvironmentUsesPublishedSDKContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/evaluation-environments/trial" || r.Method != "POST" ||
			r.Header.Get("Authorization") != "Bearer service-token" {
			t.Error("wrong Coding request")
		}
		var req struct {
			Scope    sdk.EvaluationScope
			Scenario sdk.CodingEvaluationScenario
			Phase    string
		}
		if err := json.NewDecoder(r.Body).
			Decode(&req); err != nil || req.Scope.ActorID != "actor" || req.Phase != "verify" ||
			req.Scenario.BaseHead != "head" {
			t.Error("scenario conversion lost", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"status":"Completed","baseHead":"head","patch":"diff","changed":["src/file"],"checks":[{"command":"go test","exitCode":0,"log":"passed","passed":true,"commandID":"one"}],"stopped":true}`,
			),
		)
	}))
	defer server.Close()
	client, err := NewCodingEnvironment(server.URL, "service-token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	evidence, err := client.Advance(
		context.Background(),
		"trial",
		sdk.EvaluationScope{ActorID: "actor"},
		sdk.CodingEvaluationScenario{BaseHead: "head"},
		"verify",
	)
	if err != nil || evidence.Status != "Completed" || !evidence.Stopped || len(evidence.Checks) != 1 ||
		evidence.Checks[0].Log != "passed" {
		t.Fatal(evidence, err)
	}
	binding := client.Filesystem("trial")
	if binding.URL != server.URL+"/internal/v1/evaluation-environments/trial/sandbox" ||
		binding.TokenEnv != "CODING_TOOLS_TOKEN" {
		t.Fatal(binding)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Advance(
		ctx,
		"trial",
		sdk.EvaluationScope{},
		sdk.CodingEvaluationScenario{},
		"stop",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal("cancel identity lost", err)
	}
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"EVALUATION_CONFLICT","message":"changed input"}}`))
	}))
	defer failed.Close()
	other, err := NewCodingEnvironment(failed.URL, "service-token")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Advance(
		context.Background(),
		"trial",
		sdk.EvaluationScope{},
		sdk.CodingEvaluationScenario{},
		"prepare",
	)
	var detail *serrors.Error
	if !errors.As(err, &detail) || detail.Status != 409 || detail.Code != "EVALUATION_CONFLICT" {
		t.Fatal("Coding error contract lost", err)
	}
}
