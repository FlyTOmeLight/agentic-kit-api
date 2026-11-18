package schema_test

import (
	"context"
	"net/url"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/schema"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func TestInitCreatesTargetSchemaAndIsRepeatable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	name := "test_" + durable.NewID()
	if err = admin.WithContext(ctx).Exec("CREATE SCHEMA " + name).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.WithContext(ctx).Exec("DROP SCHEMA " + name + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		pool, _ := admin.DB()
		_ = pool.Close()
	})
	for i := 0; i < 2; i++ {
		if err = schema.Init(ctx, db); err != nil {
			t.Fatalf("init %d: %v", i, err)
		}
	}
	for _, model := range []any{
		&table.Agent{}, &table.AgentVersion{}, &table.Session{}, &table.Run{},
		&table.Goal{}, &table.Evaluation{}, &table.Step{}, &table.Event{}, &table.Message{},
		&table.RunEnvironment{}, &table.Usage{},
	} {
		if !db.Migrator().HasTable(model) {
			t.Fatalf("missing table for %T", model)
		}
	}
	if !db.Migrator().HasColumn(&table.RunEnvironment{}, "saved_checkpoint") {
		t.Fatal("workspace checkpoint column missing")
	}
	if !db.Migrator().HasColumn(&table.Run{}, "waiting_step") ||
		!db.Migrator().HasColumn(&table.Run{}, "waiting_kind") {
		t.Fatal("waiting columns missing")
	}
	for _, column := range []string{
		"goal_id", "parent_run_id", "parent_step_id", "waiting_external_ref",
		"pending_steer", "compacted_summary", "first_kept_seq", "stall_mark",
		"tokens_used", "cost_usd",
	} {
		if !db.Migrator().HasColumn(&table.Run{}, column) {
			t.Fatalf("run column %s missing", column)
		}
	}
	if err = db.Exec(
		`INSERT INTO durable_run (id, tenant_id, project_id, owner_id, session_id, request_key, request_hash, spec, status, deadline, max_attempts, waiting_kind)
		 VALUES ('x','t','','o','s','k','h','{}','Queued',clock_timestamp(),1,'Bogus')`,
	).Error; err == nil {
		t.Fatal("waiting_kind constraint missing")
	}
	for i, status := range []string{"WaitingExternal", "Blocked"} {
		if err = db.Exec(
			`INSERT INTO durable_run (id, tenant_id, project_id, owner_id, session_id, request_key, request_hash, spec, status, deadline, max_attempts)
			 VALUES (?, 't', '', 'o', 's', ?, 'h', '{}', ?, clock_timestamp(), 1)`,
			"status_"+status,
			"status_key_"+status,
			status,
		).Error; err != nil {
			t.Fatalf("insert run %d with status %s: %v", i, status, err)
		}
	}
	if err = db.Exec(
		`INSERT INTO durable_run (id, tenant_id, project_id, owner_id, session_id, request_key, request_hash, spec, status, deadline, max_attempts)
		 VALUES ('bad_status','t','','o','s','bad_status_key','h','{}','Bogus',clock_timestamp(),1)`,
	).Error; err == nil {
		t.Fatal("run status constraint missing")
	}
}
