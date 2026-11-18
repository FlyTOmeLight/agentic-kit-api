package table_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"example.com/sandbox-design-contracts/internal/table"
)

var tableToStruct = map[string]any{
	"sandbox_profile_revision":  table.ProfileRevisionRow{},
	"sandbox_quota_account":     table.QuotaAccountRow{},
	"sandbox_quota_reservation": table.QuotaReservationRow{},
	"sandbox_warm_pool":         table.WarmPoolRow{},
	"sandbox_workspace_binding": table.WorkspaceBindingRow{},
	"sandbox_acquire_request":   table.AcquireRequestRow{},
	"sandbox_instance":          table.InstanceRow{},
	"sandbox_lease":             table.LeaseRow{},
	"sandbox_reuse_slot":        table.ReuseSlotRow{},
	"sandbox_action":            table.ActionRow{},
	"sandbox_execution":         table.ExecutionRow{},
	"sandbox_worktree":          table.WorktreeRow{},
	"sandbox_checkpoint":        table.CheckpointRow{},
	"sandbox_access_session":    table.AccessSessionRow{},
	"sandbox_access_ticket":     table.AccessTicketRow{},
	"sandbox_event_stream":      table.EventStreamRow{},
	"sandbox_event":             table.EventRow{},
	"sandbox_output_segment":    table.OutputSegmentRow{},
}

func TestPostgresSchemaColumnMapping(t *testing.T) {
	ddlPath := filepath.Join("..", "..", "schema", "postgres.sql")
	content, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("failed to read postgres.sql: %v", err)
	}

	tableRegex := regexp.MustCompile(`(?s)CREATE TABLE\s+(\w+)\s*\((.*?)\);`)
	matches := tableRegex.FindAllStringSubmatch(string(content), -1)
	if len(matches) != 18 {
		t.Fatalf("expected 18 tables in postgres.sql, found %d", len(matches))
	}

	for _, m := range matches {
		tableName := m[1]
		body := m[2]

		targetStruct, ok := tableToStruct[tableName]
		if !ok {
			t.Errorf("unmapped table: %s", tableName)
			continue
		}

		// Extract columns from DDL
		var ddlCols []string
		lines := strings.Split(body, "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			l = strings.TrimSuffix(l, ",")
			if l == "" || strings.HasPrefix(l, "--") {
				continue
			}
			if strings.HasPrefix(l, "PRIMARY KEY") || strings.HasPrefix(l, "CONSTRAINT") {
				continue
			}
			parts := strings.Fields(l)
			if len(parts) >= 2 {
				ddlCols = append(ddlCols, parts[0])
			}
		}

		// Extract db tags from struct
		st := reflect.TypeOf(targetStruct)
		var structCols []string
		for i := 0; i < st.NumField(); i++ {
			f := st.Field(i)
			tag := f.Tag.Get("db")
			if tag != "" {
				structCols = append(structCols, tag)
			}
		}

		if len(ddlCols) != len(structCols) {
			t.Errorf("table %s column count mismatch: DDL has %d cols, struct has %d cols",
				tableName, len(ddlCols), len(structCols))
			continue
		}

		for i := range ddlCols {
			if ddlCols[i] != structCols[i] {
				t.Errorf("table %s col[%d] mismatch: DDL='%s', struct='%s'",
					tableName, i, ddlCols[i], structCols[i])
			}
		}
	}
}
