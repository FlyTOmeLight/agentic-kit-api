package agenticv1_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestSandboxClientLifecycle(t *testing.T) {
	var calls []string
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes":
			var req sdk.AcquireSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode acquire: %v", err)
			}
			if req.RequestKey != "env-1" || req.ProfileID != "coding" || req.RunID != "run-1" {
				t.Errorf("unexpected acquire body: %+v", req)
			}
			io.WriteString(
				w,
				`{"sandbox":{"id":"sb-1","status":"Active","leaseId":"lease-1"},"lease":{"id":"lease-1","revision":1}}`,
			)
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes/sb-1/renew":
			var req sdk.RenewSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode renew: %v", err)
			}
			if req.Revision != 2 || req.RequestKey != "renew-2" {
				t.Errorf("unexpected renew body: %+v", req)
			}
			io.WriteString(w, `{"id":"lease-1","revision":2,"status":"Active"}`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes/sb-1/operations":
			var req sdk.ExecSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode exec: %v", err)
			}
			if req.RequestKey != "op-1" || req.Script != "echo hi" {
				t.Errorf("unexpected exec body: %+v", req)
			}
			io.WriteString(w, `{"id":"op-id-1","status":"Succeeded","output":"hi"}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/sandboxes/sb-1/operations/op-id-1":
			io.WriteString(w, `{"id":"op-id-1","status":"Succeeded","output":"hi"}`)
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/sandboxes/sb-1":
			io.WriteString(w, `{"id":"sb-1","status":"Destroyed"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()

	view, err := client.Sandboxes.Acquire(ctx, &sdk.AcquireSandboxRequest{
		TenantID: "t", OwnerID: "o", RequestKey: "env-1",
		ProfileID: "coding", WorkerID: "w-1", RunID: "run-1", TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Sandbox.ID != "sb-1" || view.Lease.ID != "lease-1" {
		t.Fatalf("unexpected view: %+v", view)
	}

	lease, err := client.Sandboxes.Renew(ctx, &sdk.RenewSandboxRequest{
		ID: "sb-1", Revision: 2, RequestKey: "renew-2", TTLSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Revision != 2 {
		t.Fatalf("unexpected lease: %+v", lease)
	}

	op, err := client.Sandboxes.Exec(ctx, &sdk.ExecSandboxRequest{
		ID: "sb-1", RequestKey: "op-1", Script: "echo hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != sdk.SandboxOpSucceeded || op.Output != "hi" {
		t.Fatalf("unexpected operation: %+v", op)
	}

	queried, err := client.Sandboxes.GetOperation(
		ctx,
		&sdk.GetSandboxOperationRequest{ID: "sb-1", OperationID: "op-id-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if queried.ID != "op-id-1" {
		t.Fatalf("unexpected queried operation: %+v", queried)
	}

	sb, err := client.Sandboxes.Release(ctx, "sb-1")
	if err != nil {
		t.Fatal(err)
	}
	if sb.Status != sdk.SandboxStatusDestroyed {
		t.Fatalf("unexpected sandbox: %+v", sb)
	}
	if len(calls) != 5 {
		t.Fatalf("expected 5 calls, got %v", calls)
	}
}

func TestSandboxClientRequiredSave(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			var request sdk.AcquireSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.RequiredSave {
				t.Errorf("required save missing: %+v %v", request, err)
			}
			io.WriteString(
				w,
				`{"sandbox":{"id":"sb","requiredSave":true,"status":"Active","workspaceUid":"pvc-original"}}`,
			)
		case http.MethodDelete:
			io.WriteString(
				w,
				`{"id":"sb","requiredSave":true,"status":"SaveBlocked","workspaceUid":"pvc-original","stoppedAt":"2026-11-09T07:00:00Z","version":4}`,
			)
		default:
			t.Errorf("unexpected method: %s", r.Method)
		}
	})
	if _, err := client.Sandboxes.Acquire(
		context.Background(),
		&sdk.AcquireSandboxRequest{RequiredSave: true},
	); err != nil {
		t.Fatal(err)
	}
	sandbox, err := client.Sandboxes.Release(context.Background(), "sb")
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Status != sdk.SandboxStatusSaveBlocked || sandbox.StoppedAt == nil ||
		sandbox.WorkspaceUID != "pvc-original" || sandbox.Version != 4 {
		t.Fatalf("release lost save protection state: %+v", sandbox)
	}
}
