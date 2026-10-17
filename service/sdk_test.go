package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestSDKConversation(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(
		NewRouter(
			t,
			store,
			nil,
			map[string]string{testToken: "owner", "other-token-with-24-characters": "other"},
		),
	)
	defer server.Close()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	agent, err := client.Agents.Create(
		ctx, &sdk.CreateAgentRequest{
			Name: "SDK agent",
			Config: sdk.AgentConfig{
				Model:    sdk.ModelSpec{Name: "test", BaseURL: "http://localhost:11434/v1"},
				MaxTurns: 5,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = client.Agents.Update(
		ctx,
		&sdk.UpdateAgentRequest{ID: agent.ID, Name: "updated", Revision: agent.Revision, Config: agent.Draft},
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.Agents.Publish(ctx, &sdk.PublishAgentRequest{ID: agent.ID, Revision: agent.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := client.Agents.GetVersion(ctx, &sdk.GetAgentVersionRequest{ID: agent.ID, Number: version.Number})
	if err != nil || fetched.Number != version.Number {
		t.Fatalf("version=%+v err=%v", fetched, err)
	}
	gotAgent, err := client.Agents.Get(ctx, &sdk.GetAgentRequest{ID: agent.ID})
	if err != nil || gotAgent.Name != "updated" {
		t.Fatalf("agent=%+v err=%v", gotAgent, err)
	}
	agents, err := client.Agents.List(ctx, &sdk.ListAgentsRequest{PageOptions: sdk.PageOptions{Limit: 1}})
	if err != nil || len(agents.Items) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	session, err := client.Sessions.Create(
		ctx, &sdk.CreateSessionRequest{AgentID: agent.ID, Title: "SDK conversation"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sessions.Get(ctx, &sdk.GetSessionRequest{ID: session.ID}); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.Sessions.List(
		ctx,
		&sdk.ListSessionsRequest{ListSessionsOptions: sdk.ListSessionsOptions{AgentID: agent.ID}},
	)
	if err != nil || len(sessions.Items) != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	submitted, err := client.Sessions.SendMessage(
		ctx, &sdk.SendMessageRequest{SessionID: session.ID, IdempotencyKey: "question-1", Query: "hello"},
	)
	var run *sdk.Run
	if submitted != nil {
		run = &submitted.Run
	}
	created := submitted != nil && submitted.Created

	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	replayResult, err := client.Sessions.SendMessage(
		ctx, &sdk.SendMessageRequest{SessionID: session.ID, IdempotencyKey: "question-1", Query: "hello"},
	)
	var replay *sdk.Run
	if replayResult != nil {
		replay = &replayResult.Run
	}
	created = replayResult != nil && replayResult.Created

	if err != nil || created || replay.ID != run.ID {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	_, err = client.Sessions.SendMessage(
		ctx, &sdk.SendMessageRequest{SessionID: session.ID, IdempotencyKey: "question-1", Query: "different"},
	)
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("conflict=%v", err)
	}
	claimed, err := store.Claim(ctx, "sdk-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(
		ctx,
		claimed.Lease(),
		[]byte(`{}`),
		durable.Completed,
		"hello back",
		"",
		"Completed",
	); err != nil {
		t.Fatal(err)
	}
	run, err = client.Runs.Get(ctx, &sdk.GetRunRequest{ID: run.ID})
	if err != nil || run.Status != sdk.Completed || run.Output != "hello back" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	messages, err := client.Sessions.ListMessages(
		ctx,
		&sdk.ListMessagesRequest{SessionID: session.ID, ListMessagesOptions: sdk.ListMessagesOptions{After: 1}},
	)
	if err != nil || len(messages.Items) != 1 || messages.Items[0].Message.Content != "hello back" {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	runs, err := client.Sessions.ListRuns(
		ctx,
		&sdk.ListSessionRunsRequest{SessionID: session.ID, PageOptions: sdk.PageOptions{}},
	)
	if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != run.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	stream, err := client.Runs.OpenEvents(ctx, &sdk.OpenRunEventsRequest{ID: run.ID, LastEventID: run.ID + ":1"})
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
	_, err = other.Runs.Get(ctx, &sdk.GetRunRequest{ID: run.ID})
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("owner isolation=%v", err)
	}
	nextResult, err := client.Sessions.SendMessage(
		ctx, &sdk.SendMessageRequest{SessionID: session.ID, IdempotencyKey: "question-2", Query: "next"},
	)
	var next *sdk.Run
	if nextResult != nil {
		next = &nextResult.Run
	}

	if err != nil {
		t.Fatal(err)
	}
	if err = client.Runs.Cancel(ctx, &sdk.CancelRunRequest{ID: next.ID}); err != nil {
		t.Fatal(err)
	}
	next, err = client.Runs.Get(ctx, &sdk.GetRunRequest{ID: next.ID})
	if err != nil || next.Status != sdk.Canceled {
		t.Fatalf("cancel=%+v err=%v", next, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "empty agent", call: func() error { _, e := client.Agents.Create(ctx, &sdk.CreateAgentRequest{}); return e }},
		{name: "empty session", call: func() error { _, e := client.Sessions.Create(ctx, &sdk.CreateSessionRequest{}); return e }},
		{name: "invalid revision", call: func() error { _, e := client.Agents.Publish(ctx, &sdk.PublishAgentRequest{ID: agent.ID}); return e }},
		{name: "empty answer", call: func() error {
			return client.Runs.Answer(ctx, &sdk.AnswerRunRequest{ID: run.ID, SupplyResultRequest: sdk.SupplyResultRequest{}})
		}},
		{name: "oversized step", call: func() error {
			return client.Runs.Resolve(ctx, &sdk.ResolveRunRequest{ID: run.ID, SupplyResultRequest: sdk.SupplyResultRequest{StepID: strings.Repeat("x", 129), Result: "done"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.call()
			var ae *serrors.Error
			if !errors.As(e, &ae) || ae.Status != 400 {
				t.Fatalf("validation=%v", e)
			}
		})
	}
}
