//go:build windows

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	modernc "modernc.org/sqlite"
)

func sqliteDriverName() string { return "sqlite" }

func sqliteFileURI(abs string) string {
	path := filepath.ToSlash(abs)
	if volume := filepath.VolumeName(abs); len(volume) == 2 && volume[1] == ':' {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

type moderncConnWithOptionalInterfaces interface {
	driver.Conn
	driver.ConnBeginTx
	driver.Pinger
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.SessionResetter
	driver.Validator
}

type immediateConn struct {
	moderncConnWithOptionalInterfaces
}

func (c immediateConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	// modernc omits _txlock for ReadOnly transactions; go-sqlite3 applies
	// its configured BEGIN mode to every transaction, so preserve that behavior.
	opts.ReadOnly = false
	return c.moderncConnWithOptionalInterfaces.BeginTx(ctx, opts)
}

type immediateDriver struct {
	driver driver.Driver
}

func (d immediateDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	optional, ok := conn.(moderncConnWithOptionalInterfaces)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("modernc SQLite connection is missing required driver interfaces")
	}
	return immediateConn{moderncConnWithOptionalInterfaces: optional}, nil
}

type immediateConnector struct {
	driver immediateDriver
	dsn    string
}

func (c immediateConnector) Connect(context.Context) (driver.Conn, error) {
	return c.driver.Open(c.dsn)
}

func (c immediateConnector) Driver() driver.Driver { return c.driver }

func openSQLite(dsn string) (*sql.DB, error) {
	return sql.OpenDB(immediateConnector{
		driver: immediateDriver{driver: &modernc.Driver{}},
		dsn:    dsn,
	}), nil
}
