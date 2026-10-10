// Package metadata owns schema migrations and serializes metadata mutations.
package metadata

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Queries is the common surface of the database and an active transaction.
type Queries interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Store struct {
	DB *sql.DB
	mu sync.Mutex
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	ctx := context.Background()
	for _, stmt := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA wal_autocheckpoint=256", "PRAGMA cache_size=-2048"} {
		if _, err = db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 3 {
		db.Close()
		return nil, fmt.Errorf("unsupported schema version %d", version)
	}
	if version == 0 {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, schema); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "PRAGMA user_version=1")
			return err
		})
		version = 1
	}
	if err == nil && version == 1 {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE folder_requests (
			 user_id TEXT NOT NULL REFERENCES users(id), request_key TEXT NOT NULL,
			 parent_id TEXT NOT NULL, name TEXT NOT NULL,
			 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			 PRIMARY KEY(user_id,request_key))`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "PRAGMA user_version=2")
			return err
		})
		version = 2
	}
	if err == nil && version == 2 {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE favorites (
			 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			 PRIMARY KEY(user_id,node_id));
			 CREATE INDEX favorite_node ON favorites(node_id);
			 CREATE INDEX node_recent ON nodes(created_at DESC,id) WHERE kind='file' AND deleted_at IS NULL`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "PRAGMA user_version=3")
			return err
		})
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.DB.Close() }

func IsUnique(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && (e.Code() == 2067 || e.Code() == 1555)
}

func IsDiskFull(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&255 == 13
}

var (
	ErrNotFound    = errors.New("not found")
	ErrForbidden   = errors.New("forbidden")
	ErrConflict    = errors.New("conflict")
	ErrInvalid     = errors.New("invalid request")
	ErrUnavailable = errors.New("storage unavailable")
	ErrSpace       = errors.New("insufficient storage")
)
