//go:build !sqlite_fts5 && !windows

package sqlite

import (
	"fmt"
	"os"
	"testing"
)

// TestMain fails fast with an actionable message: the mattn/go-sqlite3 driver
// only compiles the FTS5 module that migration 006 requires when the
// sqlite_fts5 build tag is set.
func TestMain(m *testing.M) {
	fmt.Fprintln(os.Stderr, "sqlite tests require FTS5: build with -tags sqlite_fts5 (see Makefile), e.g. go test -tags sqlite_fts5 ./...")
	os.Exit(1)
}
