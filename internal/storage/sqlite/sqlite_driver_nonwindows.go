//go:build !windows

package sqlite

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func sqliteDriverName() string { return "sqlite3" }

func sqliteFileURI(abs string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_busy_timeout=10000&_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate"
}

func openSQLite(dsn string) (*sql.DB, error) {
	return sql.Open(sqliteDriverName(), dsn)
}
