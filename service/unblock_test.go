package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestSDKUnblockRun(t *testing.T) {
	store := durableTestStore(t)
	run := createDurableRun(t, store)
	ctx := context.Background()
	claim, err := store.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(ctx, claim.Lease(), durable.Boundary{
		Kind:   durable.BoundaryBlock,
		State:  json.RawMessage(`{"version":3,"turn":0,"inflightModel":{"kind":"model","sequence":0}}`),
		Detail: "unknown consumption",
	}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(t, store, nil, map[string]string{testToken: "owner", "other-token": "other"})
	path := "/v1/runs/" + run.ID + "/unblock"
	for _, test := range []struct {
		name   string
		body   string
		token  string
		status int
		code   string
		query  string
	}{
		{name: "unverified tenant", body: `{"epoch":1,"reason":"accept"}`, status: 403, code: "Forbidden"},
		{name: "foreign owner", body: `{"epoch":1,"reason":"accept"}`, token: "other-token", status: 404, code: "NotFound"},
		{name: "missing epoch", body: `{"reason":"accept"}`, token: testToken, status: 400, code: "InvalidParameter"},
		{name: "invalid epoch", body: `{"epoch":0,"reason":"accept"}`, token: testToken, status: 400, code: "InvalidParameter"},
		{name: "blank reason", body: `{"epoch":1,"reason":"  "}`, token: testToken, status: 400, code: "InvalidParameter"},
		{name: "long reason", body: `{"epoch":1,"reason":"` + strings.Repeat("x", 4097) + `"}`, token: testToken, status: 400, code: "InvalidParameter"},
		{name: "unknown field", body: `{"epoch":1,"reason":"accept","id":"other"}`, token: testToken, status: 400, code: "InvalidParameter"},
		{name: "stale epoch", body: `{"epoch":99,"reason":"accept"}`, token: testToken, status: 409, code: "Conflict"},
		{name: "query cannot supply epoch", body: `{"reason":"accept"}`, token: testToken, status: 400, code: "InvalidParameter", query: "?Epoch=1"},
		{name: "query cannot replace epoch", body: `{"epoch":99,"reason":"accept"}`, token: testToken, status: 409, code: "Conflict", query: "?Epoch=1"},
		{name: "query cannot supply reason", body: `{"epoch":1}`, token: testToken, status: 400, code: "InvalidParameter", query: "?Reason=accept"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertError(t, request(router, http.MethodPost, path+test.query, test.body, test.token, ""), test.status, test.code)
			got, getErr := store.Get(ctx, "owner", run.ID)
			if getErr != nil || got.Status != durable.Blocked || got.Epoch != claim.Epoch {
				t.Fatalf("rejected request changed run: %+v, err=%v", got, getErr)
			}
		})
	}
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	unblock := &sdk.UnblockRunRequest{ID: run.ID, Epoch: claim.Epoch, Reason: "accept unknown consumption"}
	if err = client.Runs.Unblock(ctx, unblock); err != nil {
		t.Fatal(err)
	}
	got, err := client.Runs.Get(ctx, &sdk.GetRunRequest{ID: run.ID})
	if err != nil || got.Status != sdk.Queued || got.Epoch != claim.Epoch+1 {
		t.Fatalf("unblocked run=%+v err=%v", got, err)
	}
	var apiErr *serrors.Error
	if err = client.Runs.Unblock(ctx, unblock); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("duplicate unblock=%v", err)
	}
}
