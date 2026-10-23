//go:build windows

package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsSQLiteDriverSelectionAndDSN(t *testing.T) {
	if got := sqliteDriverName(); got != "sqlite" {
		t.Fatalf("sqliteDriverName() = %q, want %q", got, "sqlite")
	}
	if !contains(sql.Drivers(), sqliteDriverName()) {
		t.Fatalf("driver %q is not registered: %v", sqliteDriverName(), sql.Drivers())
	}

	fileURI := sqliteFileURI(filepath.Join(`C:\data`, "qwenchik.db"))
	if want := "file:///C:/data/qwenchik.db"; fileURI != want {
		t.Fatalf("sqliteFileURI() = %q, want %q", fileURI, want)
	}

	dsn := sqliteDSN("file:sqlite-memory-test?mode=memory&cache=shared")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse sqlite DSN: %v", err)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatalf("parse sqlite DSN query: %v", err)
	}
	wantPragmas := []string{
		"busy_timeout(10000)",
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
	}
	if got := query["_pragma"]; !reflect.DeepEqual(got, wantPragmas) {
		t.Errorf("DSN _pragma values = %v, want %v", got, wantPragmas)
	}
	if got := query.Get("_txlock"); got != "immediate" {
		t.Errorf("DSN _txlock = %q, want %q", got, "immediate")
	}
	if query.Get("mode") != "memory" || query.Get("cache") != "shared" {
		t.Errorf("DSN lost existing URI options: %s", parsed.RawQuery)
	}

	databaseFile := filepath.Join(t.TempDir(), "driver.db")
	db, err := openSQLite(sqliteDSN(sqliteFileURI(databaseFile)))
	if err != nil {
		t.Fatalf("open Windows SQLite driver: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("connect Windows SQLite driver: %v", err)
	}
	var foreignKeys, busyTimeout, synchronous int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout pragma: %v", err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous pragma: %v", err)
	}
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode pragma: %v", err)
	}
	if foreignKeys != 1 || busyTimeout != 10000 || synchronous != 1 || journalMode != "wal" {
		t.Errorf("SQLite pragmas = foreign_keys:%d busy_timeout:%d synchronous:%d journal_mode:%q; want 1, 10000, 1, %q", foreignKeys, busyTimeout, synchronous, journalMode, "wal")
	}
	if _, err := db.Exec("CREATE VIRTUAL TABLE driver_fts USING fts5(content)"); err != nil {
		t.Errorf("create FTS5 table with Windows driver: %v", err)
	}

	secondDB, err := openSQLite(sqliteDSN(sqliteFileURI(databaseFile)))
	if err != nil {
		t.Fatalf("open second Windows SQLite database: %v", err)
	}
	defer secondDB.Close()
	secondDB.SetMaxOpenConns(1)
	firstTx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read-only immediate transaction: %v", err)
	}
	defer firstTx.Rollback()
	if _, err := secondDB.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatalf("disable second connection busy timeout for lock check: %v", err)
	}
	secondTx, err := secondDB.BeginTx(context.Background(), nil)
	if err == nil {
		_ = secondTx.Rollback()
		t.Fatal("second write transaction began while read-only transaction held an immediate lock")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
