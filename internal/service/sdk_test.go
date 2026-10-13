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
			[]string{"owner"},
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
		ctx,
		sdk.CreateAgentRequest{
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
		agent.ID,
		sdk.UpdateAgentRequest{Name: "updated", Revision: agent.Revision, Config: agent.Draft},
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.Agents.Publish(ctx, agent.ID, sdk.PublishAgentRequest{Revision: agent.Revision})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := client.Agents.GetVersion(ctx, agent.ID, version.Number)
	if err != nil || fetched.Number != version.Number {
		t.Fatalf("version=%+v err=%v", fetched, err)
	}
	gotAgent, err := client.Agents.Get(ctx, agent.ID)
	if err != nil || gotAgent.Name != "updated" {
		t.Fatalf("agent=%+v err=%v", gotAgent, err)
	}
	agents, err := client.Agents.List(ctx, sdk.PageOptions{Limit: 1})
	if err != nil || len(agents.Items) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	session, err := client.Sessions.Create(
		ctx,
		sdk.CreateSessionRequest{AgentID: agent.ID, Title: "SDK conversation"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Sessions.Get(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.Sessions.List(ctx, sdk.ListSessionsOptions{AgentID: agent.ID})
	if err != nil || len(sessions.Items) != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	run, created, err := client.Sessions.SendMessage(
		ctx,
		session.ID,
		"question-1",
		sdk.SendMessageRequest{Query: "hello"},
	)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	replay, created, err := client.Sessions.SendMessage(
		ctx,
		session.ID,
		"question-1",
		sdk.SendMessageRequest{Query: "hello"},
	)
	if err != nil || created || replay.ID != run.ID {
		t.Fatalf("replay=%+v created=%v err=%v", replay, created, err)
	}
	_, _, err = client.Sessions.SendMessage(
		ctx,
		session.ID,
		"question-1",
		sdk.SendMessageRequest{Query: "different"},
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
	run, err = client.Runs.Get(ctx, run.ID)
	if err != nil || run.Status != sdk.Completed || run.Output != "hello back" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	messages, err := client.Sessions.ListMessages(ctx, session.ID, sdk.ListMessagesOptions{After: 1})
	if err != nil || len(messages.Items) != 1 || messages.Items[0].Message.Content != "hello back" {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	runs, err := client.Sessions.ListRuns(ctx, session.ID, sdk.PageOptions{})
	if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != run.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	stream, err := client.Runs.OpenEvents(ctx, run.ID, run.ID+":1")
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
	_, err = other.Runs.Get(ctx, run.ID)
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("owner isolation=%v", err)
	}
	next, _, err := client.Sessions.SendMessage(
		ctx,
		session.ID,
		"question-2",
		sdk.SendMessageRequest{Query: "next"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Runs.Cancel(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	next, err = client.Runs.Get(ctx, next.ID)
	if err != nil || next.Status != sdk.Canceled {
		t.Fatalf("cancel=%+v err=%v", next, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "empty agent", call: func() error { _, e := client.Agents.Create(ctx, sdk.CreateAgentRequest{}); return e }},
		{name: "empty session", call: func() error { _, e := client.Sessions.Create(ctx, sdk.CreateSessionRequest{}); return e }},
		{name: "invalid revision", call: func() error { _, e := client.Agents.Publish(ctx, agent.ID, sdk.PublishAgentRequest{}); return e }},
		{name: "empty answer", call: func() error { return client.Runs.Answer(ctx, run.ID, sdk.SupplyResultRequest{}) }},
		{name: "oversized step", call: func() error {
			return client.Runs.Resolve(ctx, run.ID, sdk.SupplyResultRequest{StepID: strings.Repeat("x", 129), Result: "done"})
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
