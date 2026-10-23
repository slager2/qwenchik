package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func researchLegacyDatabase(t *testing.T, path string, last int) *sql.DB {
	t.Helper()
	db, err := openSQLite(sqliteDSN(path + "?_foreign_keys=on"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range entries {
		if i >= last {
			break
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			t.Fatalf("apply fixture migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, entry.Name(), stamp(time.Now().UTC())); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func researchMigrationDataSnapshot(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT IN ('schema_migrations','research_sessions','harness_bundles') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	result := make(map[string][]string)
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		result[table] = []string{}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			legacyValues := make([]any, 0, len(values))
			for i, column := range columns {
				if strings.HasPrefix(column, "tasklease_") {
					continue
				}
				switch column {
				case "pause_reason", "resource_reason", "resume_after", "resource_probe_attempts", "terminal_outcome", "parent_id", "state_schema_version", "state_hash", "context_snapshot_hash":
					continue
				}
				legacyValues = append(legacyValues, values[i])
			}
			encoded, err := json.Marshal(legacyValues)
			if err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], string(encoded))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(result[table])
	}
	return result
}

func researchMigrationSchemaSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,COALESCE(sql,'') FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			t.Fatal(err)
		}
		result[kind+":"+name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func seedResearchMigrationLegacy(t *testing.T, db *sql.DB) {
	t.Helper()
	now := stamp(time.Now().UTC())
	for _, statement := range []string{
		`INSERT INTO tasks(id,title,description,status,created_at,updated_at) VALUES('legacy','Legacy task','Preserve legacy research','inbox',?,?)`,
		`INSERT INTO memories(id,kind,status,text,metadata_json,scope,created_at,updated_at) VALUES('legacy-memory','preference','accepted','Durable citrus memory','{}','global',?,?)`,
		`INSERT INTO task_steps(id,task_id,step_id,position,kind,description,dependencies_json,expected_output,success_criteria_json,status,created_at,updated_at) VALUES('legacy-step','legacy','fetch_1',0,'fetch','Historical source','[]','source','["source saved"]','completed',?,?)`,
	} {
		if _, err := db.Exec(statement, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO messages(id,task_id,role,content,created_at) VALUES('legacy-message','legacy','user','Historical user message',?)`,
		`INSERT INTO checkpoints(id,task_id,plan_version,state_json,last_event_seq,created_at) VALUES('legacy-checkpoint','legacy',7,'{"future_frontier":{"opaque":[1,2,3]},"search_queries":3}',1,?)`,
		`INSERT INTO events(id,task_id,seq,type,schema_version,created_at,visibility,payload_json) VALUES('legacy-event','legacy',1,'checkpoint.saved',1,?,'internal','{"plan_version":7}')`,
		`INSERT INTO artifacts(id,task_id,name,mime_type,sha256,size_bytes,storage_path,created_at) VALUES('legacy-artifact','legacy','source.txt','text/plain','source-hash',12,'legacy/legacy-artifact',?)`,
	} {
		if _, err := db.Exec(statement, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO evidence(id,task_id,provider,original_url,canonical_url,retrieved_at,content_sha256,text_artifact_id) VALUES('legacy-evidence','legacy','direct_http','https://example.org/','https://example.org/',?,'source-hash','legacy-artifact')`, now); err != nil {
		t.Fatal(err)
	}
}

func TestResearchAndRefinementMigrationsAddOnlyRequiredTablesAndPreserveAllData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration-009.db")
	db := researchLegacyDatabase(t, path, 8)
	seedResearchMigrationLegacy(t, db)
	now := stamp(time.Now().UTC())
	if _, err := db.Exec(`INSERT INTO conversations(id,title,mode,created_at,updated_at) VALUES('historical-conversation','Historical conversation','research',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET conversation_id='historical-conversation',mode='research' WHERE id='legacy'`); err != nil {
		t.Fatal(err)
	}
	beforeData := researchMigrationDataSnapshot(t, db)
	beforeSchema := researchMigrationSchemaSnapshot(t, db)
	ftsBefore := ftsSchemaSnapshot(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if after := researchMigrationDataSnapshot(t, s.db); !reflect.DeepEqual(after, beforeData) {
		t.Fatal("migration 009 changed existing table data")
	}
	afterSchema := researchMigrationSchemaSnapshot(t, s.db)
	for name, definition := range beforeSchema {
		if name == "table:tasks" || name == "table:checkpoints" {
			continue
		}
		if afterSchema[name] != definition {
			t.Fatalf("migration changed existing schema object %s", name)
		}
	}
	var addedTables []string
	for name := range afterSchema {
		if _, existed := beforeSchema[name]; !existed && strings.HasPrefix(name, "table:") {
			addedTables = append(addedTables, name)
		}
	}
	sort.Strings(addedTables)
	if !reflect.DeepEqual(addedTables, []string{"table:harness_bundles", "table:research_sessions"}) {
		t.Fatalf("unexpected added tables: %v", addedTables)
	}
	for _, operation := range []func() error{
		func() error { _, err := s.GetResearchSession(ctx, "legacy"); return err },
		func() error { _, err := s.GetRunModelSnapshot(ctx, "legacy"); return err },
		func() error { _, err := s.ReserveResearchLLMCall(ctx, "legacy"); return err },
		func() error { _, err := s.ReserveResearchHTTPAttempt(ctx, "legacy"); return err },
		func() error { _, err := s.ReserveResearchOperation(ctx, "legacy", "search", "legacy-op"); return err },
		func() error { return s.SyncResearchCheckpoint(ctx, "legacy", map[string]any{}) },
		func() error { return s.SetResearchPhase(ctx, "legacy", "search", "") },
	} {
		if err := operation(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("legacy session operation: %v", err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM research_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration backfilled legacy sessions: %d %v", count, err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(researchMigrationSchemaSnapshot(t, s.db), afterSchema) || !reflect.DeepEqual(researchMigrationDataSnapshot(t, s.db), beforeData) {
		t.Fatal("repeat migration or legacy reads changed persisted data")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 11 {
		t.Fatalf("migration ledger: %d %v", count, err)
	}
	if ftsSchemaSnapshot(t, s.db) != ftsBefore {
		t.Fatal("migration changed FTS schema or triggers")
	}
	matches, err := s.SearchMemories(ctx, "citrus", "", 5)
	if err != nil || len(matches) != 1 || matches[0].Memory.ID != "legacy-memory" {
		t.Fatalf("FTS lookup after migration: %#v %v", matches, err)
	}
	if _, err := s.UpdateMemory(ctx, "legacy-memory", "preference", "Durable raspberry memory", nil); err != nil {
		t.Fatal(err)
	}
	matches, err = s.SearchMemories(ctx, "raspberry", "", 5)
	if err != nil || len(matches) != 1 {
		t.Fatalf("FTS update trigger after migration: %#v %v", matches, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration left foreign key violations")
	}
}

func TestResearchSessionMigrationUpgradesEveryExistingVersion(t *testing.T) {
	ctx := context.Background()
	for version := 1; version <= 8; version++ {
		t.Run(fmt.Sprintf("from-%03d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db := researchLegacyDatabase(t, path, version)
			seedResearchMigrationLegacy(t, db)
			ftsBefore := ""
			if version >= 6 {
				ftsBefore = ftsSchemaSnapshot(t, db)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			task, err := s.GetTask(ctx, "legacy")
			if err != nil || task.Description != "Preserve legacy research" {
				t.Fatalf("legacy task changed: %#v %v", task, err)
			}
			messages, err := s.Messages(ctx, "legacy")
			if err != nil || len(messages) != 1 || messages[0].Content != "Historical user message" {
				t.Fatalf("legacy messages changed: %#v %v", messages, err)
			}
			steps, err := s.Steps(ctx, "legacy")
			if err != nil || len(steps) != 1 || steps[0].Status != "completed" {
				t.Fatalf("legacy steps changed: %#v %v", steps, err)
			}
			checkpoint, err := s.LatestCheckpoint(ctx, "legacy")
			if err != nil || checkpoint.PlanVersion != 7 || checkpoint.State.(map[string]any)["research_schema_version"] != nil || checkpoint.State.(map[string]any)["future_frontier"] == nil {
				t.Fatalf("legacy checkpoint changed: %#v %v", checkpoint, err)
			}
			matches, err := s.SearchMemories(ctx, "citrus", "", 5)
			if err != nil || len(matches) != 1 {
				t.Fatalf("legacy FTS data changed: %#v %v", matches, err)
			}
			if version >= 6 && ftsSchemaSnapshot(t, s.db) != ftsBefore {
				t.Fatal("existing FTS schema changed")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM research_sessions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("legacy session backfill: %d %v", count, err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 11 {
				t.Fatalf("migration ledger: %d %v", count, err)
			}
		})
	}
}
