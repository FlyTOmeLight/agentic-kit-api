// Package sandboxe2e hosts the real-cluster sandbox-center fixture shared by
// the API e2e tests and the worker evaluation takeover e2e test. It is only
// compiled into test binaries.
package sandboxe2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// Cluster is a running sandbox-center against a real Kubernetes cluster with
// its own isolated PostgreSQL schema.
type Cluster struct {
	Client   *sdk.Client
	Store    *sandboxcenter.Store
	Provider *sandboxcenter.Provider
	URL      string

	server *httptest.Server
}

// Start requires TEST_DATABASE_URL and SANDBOX_E2E_K8S_CONTEXT; it skips the
// test otherwise. The fixture cleans up its schema and HTTP server.
func Start(t *testing.T) *Cluster {
	return start(t, "")
}

// StartOnSchema joins the schema the caller already created (the DSN carries
// search_path), so the center shares the caller's durable rows.
func StartOnSchema(t *testing.T, schemaDSN string) *Cluster {
	return start(t, schemaDSN)
}

func start(t *testing.T, schemaDSN string) *Cluster {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	contextName := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if dsn == "" || contextName == "" {
		t.Skip("TEST_DATABASE_URL and SANDBOX_E2E_K8S_CONTEXT are required")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:saveblocked"
	}
	checkpoints := sandboxcenter.CheckpointConfig{
		Image:         os.Getenv("SANDBOX_E2E_SAVER_IMAGE"),
		StoreHostPath: os.Getenv("SANDBOX_E2E_CHECKPOINT_HOST_PATH"),
		StorePVC:      os.Getenv("SANDBOX_E2E_CHECKPOINT_PVC"),
	}
	if checkpoints.Image == "" {
		checkpoints.Image = "agentic-sandbox-saver:v1"
	}
	if checkpoints.StoreHostPath == "" && checkpoints.StorePVC == "" {
		checkpoints.StoreHostPath = "/private/tmp/sandbox-center-checkpoints"
	}

	ctx := context.Background()
	var db *frameworkdb.DB
	var err error
	if schemaDSN != "" {
		db, err = frameworkdb.NewDB(ctx, &frameworkdb.Config{
			Driver: frameworkdb.DriverPostgreSQL, DSN: schemaDSN, MaxOpenConns: 4, MaxIdleConns: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if sqlDB, err := db.DB(); err == nil {
				sqlDB.Close()
			}
		})
		if err := db.AutoMigrate(
			&sandboxcenter.Sandbox{},
			&sandboxcenter.Lease{},
			&sandboxcenter.Operation{},
		); err != nil {
			t.Fatal(err)
		}
	} else {
		db, err = frameworkdb.NewDB(ctx, &frameworkdb.Config{
			Driver: frameworkdb.DriverPostgreSQL, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("sandbox_e2e_%d", time.Now().UnixNano())
		if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec("DROP SCHEMA " + schema + " CASCADE")
			if sqlDB, err := db.DB(); err == nil {
				sqlDB.Close()
			}
		})
		// The framework dialector drops the search_path query parameter; scope the
		// schema with an explicit session instead of a second DSN.
		if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(
			&table.Agent{},
			&table.AgentVersion{},
			&table.Session{},
			&table.Run{},
			&table.Goal{},
			&table.Evaluation{},
			&table.Step{},
			&table.Event{},
			&table.Message{},
			&table.RunEnvironment{},
			&sandboxcenter.Sandbox{},
			&sandboxcenter.Lease{},
			&sandboxcenter.Operation{},
		); err != nil {
			t.Fatal(err)
		}
	}
	store := sandboxcenter.NewStore(db)

	provider, err := sandboxcenter.NewProvider(sandboxcenter.K8sConfig{
		Context: contextName, Namespace: "sandbox-center", GuardImage: image, MaxLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := sandboxcenter.NewService(store, provider).WithCheckpoints(checkpoints)
	reconciler := sandboxcenter.NewReconciler(store, provider, sandboxcenter.ReconcilerConfig{
		Interval: 2 * time.Second,
	}, nil).WithCheckpoints(service)
	reconcilerCtx, stopReconciler := context.WithCancel(context.Background())
	t.Cleanup(stopReconciler)
	go func() { _ = reconciler.Run(reconcilerCtx) }()
	router := gin.New()
	sandboxcenter.NewController(service).RegisterRoutes(router.Group("/v1"))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client, err := sdk.NewClient(server.URL, "local-e2e", &http.Client{Timeout: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &Cluster{Client: client, Store: store, Provider: provider, URL: server.URL, server: server}
}

// CleanupSandbox removes the workload pods and workspace PVC of a sandbox.
func (c *Cluster) CleanupSandbox(t *testing.T, sandboxID string) {
	t.Helper()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for _, name := range []string{"saver-" + sandboxID, "digest-" + sandboxID, "sandbox-" + sandboxID} {
			if _, err := c.Kubectl(cleanup, "delete", "pod", name,
				"--ignore-not-found", "--wait", "--timeout=30s"); err != nil {
				t.Error(err)
			}
		}
		if _, err := c.Kubectl(cleanup, "delete", "pvc", "workspace-"+sandboxID,
			"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
			t.Error(err)
		}
	})
}

// Kubectl runs kubectl against the fixture cluster and namespace.
func (c *Cluster) Kubectl(ctx context.Context, args ...string) (string, error) {
	return c.Provider.Kubectl(ctx, args...)
}

// WaitPodPhase polls until the pod reaches the wanted phase or fails.
func (c *Cluster) WaitPodPhase(ctx context.Context, pod, phase string) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err := c.Kubectl(ctx, "get", "pod", pod, "-o", "jsonpath={.status.phase}")
		if err == nil && strings.TrimSpace(out) == phase {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"pod %s did not reach phase %s: %s (%v)",
				pod,
				phase,
				strings.TrimSpace(out),
				err,
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
