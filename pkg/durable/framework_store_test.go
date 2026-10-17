package durable

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"

	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func TestInjectedDatabaseOwnershipAndErrors(t *testing.T) {
	existing := testStore(t)
	ctx := context.Background()
	var schema string
	if err := existing.db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	module := &frameworkdb.Module{
		Config: &frameworkdb.Config{
			Driver:       frameworkdb.DriverPostgreSQL,
			DSN:          endpoint.String(),
			MaxOpenConns: 2,
			MaxIdleConns: 1,
		},
	}
	if err := module.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer module.Close(ctx)
	store, err := New(module.DB)
	if err != nil {
		t.Fatal(err)
	}
	if module.DB.Config.TranslateError {
		t.Fatal("store mutated the shared database settings")
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	run := createRun(t, store)
	duplicate := table.Event{RunID: run.ID, Seq: 1, Kind: "Progress", Payload: json.RawMessage(`{}`)}
	if err := store.db.Create(&duplicate).Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate error lost: %v", err)
	}
	store.Close()
	if _, err := store.Get(ctx, "owner", run.ID); err != nil {
		t.Fatalf("borrowed pool closed by Store: %v", err)
	}
}
