// Package store owns the SQLite database: schema, migrations and queries.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	*sql.DB
	Path string

	settingsMu    sync.RWMutex
	settingsCache *Settings
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(8)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	db := &DB{DB: sdb, Path: path}
	if err := db.migrate(context.Background()); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) SchemaVersion() int {
	var v int
	_ = db.QueryRow("PRAGMA user_version").Scan(&v)
	return v
}

func (db *DB) migrate(ctx context.Context) error {
	cur := db.SchemaVersion()
	for i := cur; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- helpers ----------

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewID returns prefix_ + 14 random base32 characters.
func NewID(prefix string) string {
	b := make([]byte, 14)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return prefix + "_" + string(b)
}

// Time is a time.Time stored as unix seconds and rendered as RFC 3339 (null when zero).
type Time struct{ time.Time }

func Now() Time            { return Time{time.Now().UTC().Truncate(time.Second)} }
func At(t time.Time) Time  { return Time{t.UTC().Truncate(time.Second)} }
func (t Time) Unix() int64 { return t.Time.Unix() }

func (t *Time) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		t.Time = time.Time{}
	case int64:
		if x == 0 {
			t.Time = time.Time{}
		} else {
			t.Time = time.Unix(x, 0).UTC()
		}
	case float64:
		t.Time = time.Unix(int64(x), 0).UTC()
	default:
		return fmt.Errorf("store.Time: cannot scan %T", v)
	}
	return nil
}

func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return int64(0), nil
	}
	return t.Time.Unix(), nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.Format(time.RFC3339))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	p, err := time.Parse(time.RFC3339, s)
	t.Time = p
	return err
}

// JSON stores any value as a JSON text column.
type JSON[T any] struct{ V T }

func (j *JSON[T]) Scan(v any) error {
	var b []byte
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		b = []byte(x)
	case []byte:
		b = x
	default:
		return fmt.Errorf("store.JSON: cannot scan %T", v)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &j.V)
}

func (j JSON[T]) Value() (driver.Value, error) {
	b, err := json.Marshal(j.V)
	return string(b), err
}

func (j JSON[T]) MarshalJSON() ([]byte, error)  { return json.Marshal(j.V) }
func (j *JSON[T]) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &j.V) }

// Like escapes a user string for a LIKE '%...%' ESCAPE '\' pattern.
func Like(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Count runs a SELECT COUNT(*) style query.
func (db *DB) Count(q string, args ...any) int {
	var n int
	_ = db.QueryRow(q, args...).Scan(&n)
	return n
}
