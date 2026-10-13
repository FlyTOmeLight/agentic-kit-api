package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestProjectWorkspaceAuthorizationAndCandidates(t *testing.T) {
	store := integrationStore(t)
	ctx := t.Context()
	template, err := store.CreateAgent(
		ctx,
		"templates",
		"Writing",
		sdk.AgentConfig{
			SystemPrompt: "original",
			Model: sdk.ModelSpec{
				Name:      "model",
				BaseURL:   "http://private-model/v1",
				APIKeyEnv: "PRIVATE_CREDENTIAL",
			},
			MaxTurns: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PublishAgent(ctx, "templates", template.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			t.Error("missing service authentication")
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/v1/session/check":
			session, _ := body["session"].(string)
			if session != "developer" && session != "reader" {
				w.WriteHeader(401)
				return
			}
			if body["method"] != "GET" &&
				(body["csrf"] != "csrf" || !strings.HasPrefix(body["origin"].(string), "http://127.0.0.1:")) {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": session})
		case "/internal/v1/access/check":
			level := 30
			if body["user_id"] == "reader" {
				level = 20
			}
			allowed := !revoked.Load() && body["tenant_id"] != "denied" &&
				(body["action"] == "read" || level >= 30)
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": allowed, "level": level})
		default:
			w.WriteHeader(404)
		}
	}))
	defer tenant.Close()
	cfg := platform.Config{
		TenantURL:     tenant.URL,
		TenantToken:   strings.Repeat("t", 32),
		TemplateOwner: "templates",
		Templates:     []platform.Template{{ID: "writing", Name: "Writing", AgentID: template.ID, Version: 1}},
	}
	server := httptest.NewServer(
		NewPlatformRouter(t, store, nil, []string{"templates"}, map[string]string{testToken: "templates"}, cfg),
	)
	defer server.Close()
	client := func(tenant, project, user, csrf string) *sdk.ProjectClient {
		c, e := sdk.NewProjectClient(server.URL, tenant, project, user, csrf, server.Client())
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	dev := client("tenant", "project", "developer", "csrf")
	reader := client("tenant", "project", "reader", "csrf")
	foreign := client("tenant", "foreign", "developer", "csrf")
	assertCode := func(err error, code int) {
		t.Helper()
		var e *serrors.Error
		if !errors.As(err, &e) || e.Status != code {
			t.Fatalf("wanted %d, got %v", code, err)
		}
	}
	rows, err := dev.Agents(ctx, sdk.PageOptions{})
	if err != nil || len(rows.Templates) != 1 || len(rows.Items) != 0 || !rows.CanEdit {
		t.Fatalf("list %+v %v", rows, err)
	}
	preview := rows.Templates[0]
	if preview.Version != 1 || preview.Prompt != "original" || preview.MaxTurns != 4 || len(preview.Tools) != 0 {
		t.Fatalf("writing template preview %+v", preview)
	}
	previewJSON, _ := json.Marshal(rows.Templates)
	if strings.Contains(string(previewJSON), "private-model") ||
		strings.Contains(string(previewJSON), "PRIVATE_CREDENTIAL") {
		t.Fatal("template connection leaked")
	}
	a, err := dev.CreateAgent(ctx, sdk.CreateProjectAgentRequest{TemplateID: "writing", Name: "Project writer"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "private-model") || strings.Contains(string(raw), "PRIVATE_CREDENTIAL") {
		t.Fatal("connection leaked")
	}
	_, err = reader.CreateAgent(ctx, sdk.CreateProjectAgentRequest{TemplateID: "writing", Name: "denied"})
	assertCode(err, 403)
	_, err = client("tenant", "project", "developer", "wrong").Candidate(ctx, a.ID, a.Revision)
	assertCode(err, 403)
	_, err = client("denied", "project", "developer", "csrf").Agent(ctx, a.ID)
	assertCode(err, 403)
	_, err = foreign.Agent(ctx, a.ID)
	assertCode(err, 404)
	_, err = client("other-tenant", "project", "developer", "csrf").Agent(ctx, a.ID)
	assertCode(err, 404)
	v, err := dev.Candidate(ctx, a.ID, 1)
	if err != nil || v.Number != 1 {
		t.Fatal(v, err)
	}
	a, err = dev.UpdateAgent(
		ctx,
		a.ID,
		sdk.UpdateProjectAgentRequest{Name: a.Name, Revision: 1, Prompt: "new prompt", MaxTurns: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dev.UpdateAgent(
		ctx,
		a.ID,
		sdk.UpdateProjectAgentRequest{Name: a.Name, Revision: 1, Prompt: "stale", MaxTurns: 4},
	)
	assertCode(err, 409)
	old, err := reader.Version(ctx, a.ID, 1)
	if err != nil || old.Prompt != "original" {
		t.Fatalf("candidate mutated %+v %v", old, err)
	}
	v, err = dev.Candidate(ctx, a.ID, a.Revision)
	if err != nil || v.Number != 2 {
		t.Fatal(v, err)
	}
	// The existing runtime owner cannot see this project clone or submit chat against it.
	runtime, _ := sdk.NewClient(server.URL, testToken, server.Client())
	_, err = runtime.Agents.Get(ctx, a.ID)
	assertCode(err, 404)
	suite := sdk.EvaluationSuite{
		SchemaVersion: 1,
		Name:          "Writing",
		Version:       "1",
		Target: sdk.EvaluationTarget{
			Kind:         "Agentic",
			AgentID:      a.ID,
			AgentVersion: 1,
			Capabilities: []string{"output"},
		},
		Scenario: sdk.EvaluationScenario{Kind: "single_turn", TimeoutSeconds: 60},
		Dataset: sdk.EvaluationDataset{
			Name:    "Cases",
			Version: "1",
			Cases:   []sdk.EvaluationCase{{ID: "one", Input: "write"}},
		},
		Evaluators: []sdk.EvaluatorSpec{
			{ID: "json", Version: "1", Kind: "json_valid", Scope: "output", Requires: []string{"output"}},
		},
		Metrics: []sdk.EvaluationMetric{
			{ID: "json", Evaluator: "json", Aggregate: "pass_rate", MinimumScore: 1, MinimumCoverage: 1},
		},
	}

	trialReq := sdk.StartProjectTrialRequest{AgentVersion: 1, Input: "Try original prompt", TimeoutSeconds: 60}
	trial, created, err := dev.StartTrial(ctx, a.ID, "try-one", trialReq)
	if err != nil || !created || trial.Input != trialReq.Input || trial.Run.ConfigVersion != "1" {
		t.Fatal(trial, err)
	}
	retriedTrial, created, err := dev.StartTrial(ctx, a.ID, "try-one", trialReq)
	if err != nil || created || retriedTrial.Run.ID != trial.Run.ID {
		t.Fatal("trial retry", err)
	}
	readTrial, err := reader.Trial(ctx, a.ID, trial.Run.ID)
	if err != nil || readTrial.Run.ID != trial.Run.ID {
		t.Fatal(readTrial, err)
	}
	_, err = foreign.Trial(ctx, a.ID, trial.Run.ID)
	assertCode(err, 404)
	_, err = reader.Trial(ctx, template.ID, trial.Run.ID)
	assertCode(err, 404)
	_, _, err = reader.StartTrial(ctx, a.ID, "denied-trial", trialReq)
	assertCode(err, 403)
	_, _, err = dev.StartTrial(ctx, template.ID, "unowned-trial", trialReq)
	assertCode(err, 404)
	err = reader.CancelTrial(ctx, a.ID, trial.Run.ID)
	assertCode(err, 403)
	if err = dev.CancelTrial(ctx, a.ID, trial.Run.ID); err != nil {
		t.Fatal(err)
	}
	readTrial, err = dev.Trial(ctx, a.ID, trial.Run.ID)
	if err != nil || readTrial.Run.Status != sdk.Canceled {
		t.Fatal(readTrial, err)
	}
	// Plans survive a new client and repeated migrations without creating Runs.
	plan, err := dev.SaveEvaluationPlan(ctx, "regression-plan", sdk.SaveEvaluationPlanRequest{Suite: suite})
	if err != nil || plan.Revision != 1 {
		t.Fatalf("save plan: %+v %v", plan, err)
	}
	replayPlan, err := dev.SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Suite: suite})
	if err != nil || replayPlan.Revision != 1 {
		t.Fatal("plan retry duplicated", err)
	}
	plans, err := reader.EvaluationPlans(ctx, sdk.PageOptions{})
	if err != nil || len(plans.Items) != 1 {
		t.Fatal(plans, err)
	}
	beforeRuns, err := dev.Evaluations(ctx, sdk.PageOptions{})
	if err != nil || len(beforeRuns.Items) != 0 {
		t.Fatal("saving a plan executed it", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := client("tenant", "project", "developer", "csrf").EvaluationPlan(ctx, plan.ID)
	if err != nil || restored.Suite.Dataset.Cases[0].Input != "write" {
		t.Fatal(restored, err)
	}
	changed := suite
	changed.Name = "Updated plan"
	updatedPlan, err := dev.SaveEvaluationPlan(
		ctx,
		plan.ID,
		sdk.SaveEvaluationPlanRequest{Revision: 1, Suite: changed},
	)
	if err != nil || updatedPlan.Revision != 2 {
		t.Fatal(updatedPlan, err)
	}
	_, err = dev.SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Revision: 1, Suite: suite})
	assertCode(err, 409)
	_, err = foreign.EvaluationPlan(ctx, plan.ID)
	assertCode(err, 404)
	_, err = foreign.SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Revision: 2, Suite: suite})
	assertCode(err, 404)
	_, err = reader.SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Revision: 2, Suite: suite})
	assertCode(err, 403)
	_, err = client(
		"tenant",
		"project",
		"developer",
		"wrong",
	).SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Revision: 2, Suite: suite})
	assertCode(err, 403)
	invalidPlan := suite
	invalidPlan.Target.AgentID = template.ID
	_, err = dev.SaveEvaluationPlan(ctx, "injected", sdk.SaveEvaluationPlanRequest{Suite: invalidPlan})
	assertCode(err, 404)
	e, created, err := dev.Start(ctx, "stable", sdk.StartProjectEvaluationRequest{Suite: suite})
	if err != nil || !created {
		t.Fatal(err)
	}

	_, err = dev.Trial(ctx, a.ID, e.Trials[0].RunID)
	assertCode(err, 404)
	rawTrial, _ := json.Marshal(trial)
	if strings.Contains(string(rawTrial), "private-model") ||
		strings.Contains(string(rawTrial), "PRIVATE_CREDENTIAL") {
		t.Fatal("trial leaked config")
	}
	if e.Scope.ActorID != "developer" || e.Scope.TenantID != "tenant" || e.Scope.ProjectID != "project" {
		t.Fatal("untrusted scope", e.Scope)
	}
	replay, created, err := dev.Start(ctx, "stable", sdk.StartProjectEvaluationRequest{Suite: suite})
	if err != nil || created || replay.ID != e.ID {
		t.Fatal("not idempotent", err)
	}

	changed.Name = "Edited after experiment"
	_, err = dev.SaveEvaluationPlan(ctx, plan.ID, sdk.SaveEvaluationPlanRequest{Revision: 2, Suite: changed})
	if err != nil {
		t.Fatal(err)
	}
	frozenExperiment, err := dev.Evaluation(ctx, e.ID)
	if err != nil || frozenExperiment.Suite.Name != suite.Name {
		t.Fatal("plan edit changed experiment", err)
	}
	suite.Target.AgentVersion = 2
	next, _, err := dev.Start(ctx, "candidate", sdk.StartProjectEvaluationRequest{Suite: suite})
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.Evaluation(ctx, e.ID)
	assertCode(err, 404)
	err = foreign.Cancel(ctx, e.ID)
	assertCode(err, 404)
	_, _, err = reader.Start(ctx, "forbidden", sdk.StartProjectEvaluationRequest{Suite: suite})
	assertCode(err, 403)
	trace, err := reader.Trace(ctx, e.ID, e.Trials[0].ID, 0)
	if err != nil || trace.Run.ConfigVersion != "1" {
		t.Fatal(trace, err)
	}
	_, err = reader.Trace(ctx, e.ID, next.Trials[0].ID, 0)
	assertCode(err, 404)
	if err = dev.Cancel(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err = dev.Cancel(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	comparison, err := reader.Compare(ctx, next.ID, e.ID)
	if err != nil || len(comparison.Cases) != 1 {
		t.Fatal(comparison, err)
	}
	suite.Target.AgentID = template.ID
	_, _, err = dev.Start(ctx, "template-injection", sdk.StartProjectEvaluationRequest{Suite: suite})
	assertCode(err, 404)
	// Strict request DTOs reject identity and connection injection.
	for _, body := range []string{`{"name":"x","revision":2,"prompt":"x","maxTurns":4,"model":{"baseURL":"http://attacker"}}`, `{"name":"x","revision":2,"prompt":"x","maxTurns":4,"actorID":"admin"}`} {
		req, _ := http.NewRequest(
			"PUT",
			server.URL+"/api/v1/tenants/tenant/projects/project/agents/"+a.ID,
			strings.NewReader(body),
		)
		req.AddCookie(&http.Cookie{Name: "forge_session", Value: "developer"})
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-CSRF-Token", "csrf")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal("injection accepted", resp.StatusCode)
		}
	}
	revoked.Store(true)
	_, err = reader.Evaluation(ctx, e.ID)
	assertCode(err, 403)
	_, err = reader.Trace(ctx, e.ID, e.Trials[0].ID, 0)
	assertCode(err, 403)
	revoked.Store(false)
	// Migration remains repeatable with project data present.
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := dev.Agent(ctx, a.ID)
	if err != nil || got.Prompt != "new prompt" {
		t.Fatal(got, err)
	}
}

func TestProjectCodingTemplateAndScenario(t *testing.T) { testCodingTemplateAndScenario(t, false) }

func TestEnterpriseCodingTemplateAndScenario(t *testing.T) { testCodingTemplateAndScenario(t, true) }

func testCodingTemplateAndScenario(t *testing.T, workspace bool) {
	store := integrationStore(t)
	ctx := t.Context()
	template, err := store.CreateAgent(
		ctx,
		"templates",
		"Coding",
		sdk.AgentConfig{
			SystemPrompt: "fix code",
			Model:        sdk.ModelSpec{Name: "model", BaseURL: "http://private/v1"},
			MaxTurns:     10,
			Toolsets:     []string{"filesystem"},
			Tools: []sdk.ToolSpec{
				{
					Name:        "finish",
					Description: "finish",
					Parameters:  json.RawMessage(`{"type":"object"}`),
					URL:         "http://business/internal/v1/tools/finish",
					Policy:      sdk.Idempotent,
					EndRun:      true,
				},
				{
					Name:        "run_checks",
					Description: "checks",
					Parameters:  json.RawMessage(`{"type":"object"}`),
					URL:         "http://business/internal/v1/tools/run_checks",
					Policy:      sdk.ReadOnly,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PublishAgent(ctx, "templates", template.ID, template.Revision); err != nil {
		t.Fatal(err)
	}
	var releaseLevel atomic.Int32
	releaseLevel.Store(30)
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "session/check") {
			io.WriteString(w, `{"id":"developer"}`)
		} else if strings.HasSuffix(r.URL.Path, "workspace/check") {
			fmt.Fprintf(
				w,
				`{"allowed":true,"canEdit":true,"projects":[{"id":"project","name":"Repo","level":%d},{"id":"second","name":"Second repo","level":%d}]}`,
				releaseLevel.Load(),
				releaseLevel.Load(),
			)
		} else {
			fmt.Fprintf(w, `{"allowed":true,"level":%d}`, releaseLevel.Load())
		}
	}))
	defer tenant.Close()
	cfg := platform.Config{
		TenantURL:     tenant.URL,
		TenantToken:   strings.Repeat("t", 32),
		TemplateOwner: "templates",
		CodingURL:     "http://coding",
		CodingToken:   strings.Repeat("c", 32),
		Templates:     []platform.Template{{ID: "coding", Name: "Coding", AgentID: template.ID, Version: 1}},
	}
	server := httptest.NewServer(
		NewPlatformRouter(t, store, nil, []string{"templates"}, map[string]string{testToken: "templates"}, cfg),
	)
	defer server.Close()
	client, err := sdk.NewProjectClient(server.URL, "tenant", "project", "session", "csrf", server.Client())
	if workspace {
		client, err = sdk.NewWorkspaceClient(server.URL, "tenant", "session", "csrf", server.Client())
	}
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.Agents(ctx, sdk.PageOptions{})
	if workspace && err == nil {
		var tpls *sdk.Page[sdk.ProjectTemplate]
		tpls, err = client.Templates(ctx)
		if err == nil {
			list.Templates = tpls.Items
		}
	}
	if err != nil || len(list.Templates) != 1 || list.Templates[0].Kind != "Coding" {
		t.Fatal(list, err)
	}
	preview := list.Templates[0]
	if preview.Version != 1 || preview.Prompt != "fix code" || preview.MaxTurns != 10 ||
		strings.Join(preview.Tools, ",") != "filesystem,finish,run_checks" {
		t.Fatalf("coding template preview %+v", preview)
	}
	previewJSON, _ := json.Marshal(list.Templates)
	if strings.Contains(string(previewJSON), "http://") || strings.Contains(string(previewJSON), "TokenEnv") {
		t.Fatal("coding template connection leaked")
	}
	agent, err := client.CreateAgent(
		ctx,
		sdk.CreateProjectAgentRequest{TemplateID: "coding", Name: "project coder"},
	)
	if err != nil || agent.Kind != "Coding" || len(agent.Tools) != 3 {
		t.Fatal(agent, err)
	}
	version, err := client.Candidate(ctx, agent.ID, agent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	suite := sdk.EvaluationSuite{
		SchemaVersion: 1,
		Name:          "coding",
		Version:       "1",
		Target: sdk.EvaluationTarget{
			Kind:         "Agentic",
			AgentID:      agent.ID,
			AgentVersion: version.Number,
			Capabilities: []string{"output", "filesystem", "checks"},
		},
		Scenario: sdk.EvaluationScenario{
			Kind:           "coding",
			TimeoutSeconds: 60,
			Coding: &sdk.CodingEvaluationScenario{
				BaseHead:     strings.Repeat("a", 40),
				AllowedPaths: []string{"src"},
				Checks:       []string{"python3 -m unittest"},
			},
		},
		Dataset: sdk.EvaluationDataset{
			Name:    "cases",
			Version: "1",
			Cases:   []sdk.EvaluationCase{{ID: "one", Input: "fix addition"}},
		},
		Evaluators: []sdk.EvaluatorSpec{
			{
				ID:       "tests",
				Version:  "1",
				Kind:     "checks_passed",
				Scope:    "checks",
				Requires: []string{"checks"},
			},
		},
		Metrics: []sdk.EvaluationMetric{
			{ID: "pass", Evaluator: "tests", Aggregate: "pass_rate", MinimumScore: 1, MinimumCoverage: 1},
		},
	}
	if workspace {
		d, e := client.SaveDataset(
			ctx,
			"coding-cases",
			sdk.SaveDatasetRequest{Name: suite.Dataset.Name, Cases: suite.Dataset.Cases},
		)
		if e != nil {
			t.Fatal(e)
		}
		v, e := client.PublishDataset(ctx, d.ID, d.Revision)
		if e != nil {
			t.Fatal(e)
		}
		suite.Dataset = v.Snapshot
		if _, _, e = client.Start(
			ctx,
			"missing-project",
			sdk.StartProjectEvaluationRequest{Suite: suite},
		); e == nil {
			t.Fatal("missing Coding project accepted")
		}
		if _, _, e = client.Start(
			ctx,
			"forbidden-project",
			sdk.StartProjectEvaluationRequest{ProjectID: "forbidden", Suite: suite},
		); e == nil {
			t.Fatal("unauthorized Coding project accepted")
		}
	}
	out, _, err := client.Start(
		ctx,
		"coding-start",
		sdk.StartProjectEvaluationRequest{ProjectID: "project", Suite: suite},
	)
	if err != nil || len(out.Trials) != 1 || out.Trials[0].Run != nil ||
		out.Trials[0].Coding.Status != "Preparing" {
		t.Fatal(out, err)
	}
	// The project namespace owns the copied version and its rebound tools.
	identity, _ := json.Marshal([]string{"tenant", "project"})
	owner := fmt.Sprintf("forge-project:%x", sha256.Sum256(identity))
	if workspace {
		owner = fmt.Sprintf("forge-tenant:%x", sha256.Sum256([]byte("tenant")))
	}
	versions, err := store.AgentVersion(ctx, owner, agent.ID, version.Number)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range versions.Config.Tools {
		if !strings.HasPrefix(tool.URL, "http://coding/internal/v1/evaluation-tools/") ||
			tool.TokenEnv != "CODING_TOOLS_TOKEN" {
			t.Fatal("business tool leaked into evaluation", tool.Name)
		}
	}

	approvalReq := sdk.CodingReleaseRequest{
		TenantID:     "tenant",
		ProjectID:    "project",
		ActorID:      "developer",
		ExperimentID: out.ID,
		AgentID:      agent.ID,
		AgentVersion: version.Number,
	}
	callApproval := func(req sdk.CodingReleaseRequest, token string, want int) sdk.CodingReleaseApproval {
		t.Helper()
		raw, _ := json.Marshal(req)
		request, _ := http.NewRequestWithContext(
			ctx,
			"POST",
			server.URL+"/internal/v1/coding-release-approval",
			strings.NewReader(string(raw)),
		)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("approval want %d got %d: %s", want, response.StatusCode, body)
		}
		var result sdk.CodingReleaseApproval
		if want == 200 {
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(string(body), "private/v1") ||
			strings.Contains(string(body), "CODING_TOOLS_TOKEN") {
			t.Fatal("approval leaked connection")
		}
		return result
	}
	callApproval(approvalReq, "missing", 401)
	callApproval(approvalReq, testToken, 403)
	releaseLevel.Store(40)
	callApproval(approvalReq, testToken, 409)
	complete := func(id string) {
		t.Helper()
		env := releaseEnvironment{}
		if err := store.ReconcileCodingEvaluations(ctx, env); err != nil {
			t.Fatal(err)
		}
		run, err := store.Claim(ctx, "release-worker", time.Minute)
		if err != nil || run == nil {
			t.Fatal(run, err)
		}
		if err := store.Save(
			ctx,
			run.Lease(),
			json.RawMessage(`{}`),
			durable.Completed,
			"completed",
			"",
			"RunCompleted",
		); err != nil {
			t.Fatal(err)
		}
		if err := store.ReconcileCodingEvaluations(ctx, env); err != nil {
			t.Fatal(err)
		}
		if err := store.ReconcileEvaluation(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	complete(out.ID)
	approval := callApproval(approvalReq, testToken, 200)
	repeated := callApproval(approvalReq, testToken, 200)
	if approval.RuntimeAgentID != repeated.RuntimeAgentID || approval.RuntimeVersion != 1 ||
		approval.AgentVersion != version.Number {
		t.Fatal("unstable release", approval, repeated)
	}
	business, err := store.AgentVersion(ctx, "templates", approval.RuntimeAgentID, 1)
	if err != nil || business.Config.SystemPrompt != versions.Config.SystemPrompt ||
		business.Config.MaxTurns != versions.Config.MaxTurns {
		t.Fatal(business, err)
	}
	for _, tool := range business.Config.Tools {
		if !strings.HasPrefix(tool.URL, "http://coding/internal/v1/tools/") {
			t.Fatal("evaluation adapter escaped", tool.Name)
		}
	}
	original, err := store.AgentVersion(ctx, owner, agent.ID, version.Number)
	if err != nil || !strings.Contains(original.Config.Tools[0].URL, "evaluation-tools") {
		t.Fatal("candidate mutated", err)
	}
	wrongVersion := approvalReq
	wrongVersion.AgentVersion++
	callApproval(wrongVersion, testToken, 409)
	foreign := approvalReq
	foreign.ProjectID = "foreign"
	if workspace {
		callApproval(foreign, testToken, 409)
	} else {
		callApproval(foreign, testToken, 404)
	}
	if workspace {
		// One enterprise Agent can be evaluated independently for a second project.
		second, _, e := client.Start(
			ctx,
			"second-project",
			sdk.StartProjectEvaluationRequest{ProjectID: "second", Suite: suite},
		)
		if e != nil {
			t.Fatal(e)
		}
		complete(second.ID)
		secondReq := approvalReq
		secondReq.ProjectID = "second"
		secondReq.ExperimentID = second.ID
		secondApproval := callApproval(secondReq, testToken, 200)
		if secondApproval.AgentID != approval.AgentID ||
			secondApproval.RuntimeAgentID != approval.RuntimeAgentID {
			t.Fatal("enterprise candidate was duplicated per project")
		}
		if _, e = client.SaveEvaluationPlan(
			ctx,
			"restricted-plan",
			sdk.SaveEvaluationPlanRequest{ProjectID: "project", Suite: suite},
		); e != nil {
			t.Fatal(e)
		}
		releaseLevel.Store(0)
		for _, id := range []string{out.ID, second.ID} {
			if _, e = client.Evaluation(ctx, id); e == nil {
				t.Fatal("revoked project report exposed")
			}
			if e = client.Cancel(ctx, id); e == nil {
				t.Fatal("revoked cancellation accepted")
			}
		}
		listed, e := client.Evaluations(ctx, sdk.PageOptions{})
		if e != nil || len(listed.Items) != 0 {
			t.Fatal("restricted report list", listed, e)
		}
		plans, e := client.EvaluationPlans(ctx, sdk.PageOptions{})
		if e != nil || len(plans.Items) != 0 {
			t.Fatal("restricted plan list", plans, e)
		}
		if _, e = client.EvaluationPlan(ctx, "restricted-plan"); e == nil {
			t.Fatal("restricted plan leaked")
		}
		if _, e = client.Agent(ctx, agent.ID); e != nil {
			t.Fatal("repository revocation removed enterprise Agent", e)
		}
		releaseLevel.Store(40)
	}
	suite.Metrics[0].MinimumScore = 0
	weak, _, err := client.Start(
		ctx,
		"weak-gate",
		sdk.StartProjectEvaluationRequest{ProjectID: "project", Suite: suite},
	)
	if err != nil {
		t.Fatal(err)
	}
	complete(weak.ID)
	weakReq := approvalReq
	weakReq.ExperimentID = weak.ID
	callApproval(weakReq, testToken, 409)
	releaseLevel.Store(30)
	callApproval(approvalReq, testToken, 403)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	releaseLevel.Store(40)
	callApproval(approvalReq, testToken, 200)
	suite.Scenario.Filesystem = &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://arbitrary",
		TokenEnv:  "SECRET",
		Workspace: "/",
	}
	if _, _, err = client.Start(
		ctx,
		"injected",
		sdk.StartProjectEvaluationRequest{ProjectID: "project", Suite: suite},
	); err == nil {
		t.Fatal("browser environment injection accepted")
	}
}

type releaseEnvironment struct{}

func (releaseEnvironment) Filesystem(id string) *sdk.FilesystemBinding {
	return &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://coding/" + id,
		TokenEnv:  "CODING_TOOLS_TOKEN",
		Workspace: "/workspace/repo",
	}
}

func (releaseEnvironment) Advance(
	_ context.Context,
	_ string,
	_ sdk.EvaluationScope,
	s sdk.CodingEvaluationScenario,
	phase string,
) (*sdk.CodingEvaluationEvidence, error) {
	result := &sdk.CodingEvaluationEvidence{Status: "Ready", BaseHead: s.BaseHead}
	if phase == "verify" {
		result.Status = "Completed"
		result.Stopped = true
		for _, command := range s.Checks {
			result.Checks = append(result.Checks, sdk.CodingEvaluationCheck{Command: command, ExitCode: 0})
		}
	}
	return result, nil
}
