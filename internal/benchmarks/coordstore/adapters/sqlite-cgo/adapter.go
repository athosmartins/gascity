//go:build cgo && sqlite_cgo

// Package sqlitecgo provides a mattn/go-sqlite3-backed StoreAdapter for the
// coordination-store benchmark sweep.
package sqlitecgo

import (
	"github.com/gastownhall/gascity/internal/benchmarks/coordstore/adapters/sqlite"
	_ "github.com/mattn/go-sqlite3" // CGo SQLite driver, isolated behind the sqlite_cgo build tag.
)

// New returns a SQLite adapter that uses mattn/go-sqlite3 with FULL
// synchronous WAL commits.
func New() *sqlite.Adapter {
	return sqlite.NewWithDriver("sqlite3", sqlite.FullSyncPragmas, "scg")
}
