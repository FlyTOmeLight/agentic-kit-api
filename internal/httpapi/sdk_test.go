package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
)

func TestSDKConversation(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(NewRouter(store, nil, []string{"owner"}, map[string]string{testToken: "owner", "other-token-with-24-characters": "other"}))
	defer server.Close()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	agent, err := client.CreateAgent(ctx, sdk.CreateAgentRequest{Name: "SDK agent", Config: sdk.AgentConfig{Model: sdk.ModelSpec{Name: "test", BaseURL: "http://localhost:11434/v1"}, MaxTurns: 5}})
	if err != nil {
		t.Fatal(err)
	}
	agent, err = client.UpdateAgent(ctx, agent.ID, sdk.UpdateAgentRequest{Name: "updated", Revision: agent.Revision, Config: agent.Draft})
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.PublishAgent(ctx, agent.ID, sdk.PublishAgentRequest{Revision: agent.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := client.GetAgentVersion(ctx, agent.ID, version.Number)
	if err != nil || fetched.Number != version.Number {
		t.Fatalf("version=%+v err=%v", fetched, err)
	}
	gotAgent, err := client.GetAgent(ctx, agent.ID)
	if err != nil || gotAgent.Name != "updated" {
		t.Fatalf("agent=%+v err=%v", gotAgent, err)
	}
	agents, err := client.ListAgents(ctx, sdk.PageOptions{Limit: 1})
	if err != nil || len(agents.Items) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	session, err := client.CreateSession(ctx, sdk.CreateSessionRequest{AgentID: agent.ID, Title: "SDK conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.ListSessions(ctx, sdk.ListSessionsOptions{AgentID: agent.ID})
	if err != nil || len(sessions.Items) != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	run, created, err := client.SendMessage(ctx, session.ID, "question-1", sdk.SendMessageRequest{Query: "hello"})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	replay, created, err := client.SendMessage(ctx, session.ID, "question-1", sdk.SendMessageRequest{Query: "hello"})
	if err != nil || created || replay.ID != run.ID {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	_, _, err = client.SendMessage(ctx, session.ID, "question-1", sdk.SendMessageRequest{Query: "different"})
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		t.Fatalf("conflict=%v", err)
	}
	claimed, err := store.Claim(ctx, "sdk-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, claimed.Lease(), []byte(`{}`), durable.Completed, "hello back", "", "Completed"); err != nil {
		t.Fatal(err)
	}
	run, err = client.GetRun(ctx, run.ID)
	if err != nil || run.Status != sdk.Completed || run.Output != "hello back" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	messages, err := client.ListMessages(ctx, session.ID, sdk.ListMessagesOptions{After: 1})
	if err != nil || len(messages.Items) != 1 || messages.Items[0].Message.Content != "hello back" {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	runs, err := client.ListSessionRuns(ctx, session.ID, sdk.PageOptions{})
	if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != run.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	stream, err := client.OpenRunEvents(ctx, run.ID, run.ID+":1")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream)
	stream.Close()
	if readErr != nil || !strings.Contains(string(data), "event:Completed") {
		t.Fatalf("events=%s err=%v", data, readErr)
	}
	other, err := sdk.NewClient(server.URL, "other-token-with-24-characters", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.GetRun(ctx, run.ID)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Fatalf("owner isolation=%v", err)
	}
	next, _, err := client.SendMessage(ctx, session.ID, "question-2", sdk.SendMessageRequest{Query: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.CancelRun(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	next, err = client.GetRun(ctx, next.ID)
	if err != nil || next.Status != sdk.Canceled {
		t.Fatalf("cancel=%+v err=%v", next, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"empty agent", func() error { _, e := client.CreateAgent(ctx, sdk.CreateAgentRequest{}); return e }},
		{"empty session", func() error { _, e := client.CreateSession(ctx, sdk.CreateSessionRequest{}); return e }},
		{"invalid revision", func() error { _, e := client.PublishAgent(ctx, agent.ID, sdk.PublishAgentRequest{}); return e }},
		{"empty answer", func() error { return client.AnswerRun(ctx, run.ID, sdk.SupplyResultRequest{}) }},
		{"oversized step", func() error {
			return client.ResolveRun(ctx, run.ID, sdk.SupplyResultRequest{StepID: strings.Repeat("x", 129), Result: "done"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.call()
			var ae *sdk.APIError
			if !errors.As(e, &ae) || ae.StatusCode != 400 {
				t.Fatalf("validation=%v", e)
			}
		})
	}
}
