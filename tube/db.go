package tube

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

var db *sql.DB

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface {
	Scan(dest ...any) error
}

func Init(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	conn, err := Open(ctx, path)
	if err != nil {
		return err
	}
	db = conn
	return nil
}

func Open(ctx context.Context, path string) (*sql.DB, error) {
	params := url.Values{}
	params.Set("_txlock", "immediate")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(10000)")
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "synchronous(NORMAL)")
	conn, err := sql.Open("sqlite", "file:"+path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}
	return conn, nil
}

func Close() error {
	if db == nil {
		return nil
	}
	return db.Close()
}

func migrate(ctx context.Context, conn *sql.DB) error {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(names); i++ {
		stmts, err := migrationFS.ReadFile(names[i])
		if err != nil {
			return err
		}
		err = withTx(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(stmts)); err != nil {
				return fmt.Errorf("%s: %w", names[i], err)
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func withTx(ctx context.Context, conn *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return withTx(ctx, db, fn)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func encode(v any) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func incCounter(ctx context.Context, id string, n int) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO counters (id, count) VALUES (?, ?)
		ON CONFLICT (id) DO UPDATE SET count = count + excluded.count`,
		id, n)
	return err
}
