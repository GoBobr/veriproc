package console

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the console-local SQLite connection. It is intentionally
// independent of the upstream veriproc control-plane store.
type DB struct {
	db *sql.DB
}

// OpenDB connects to a console-local SQLite database identified by a DSN of
// the form `file:relative/path.db` or `sqlite:///abs/path.db`.
func OpenDB(dsn string) (*DB, error) {
	if dsn == "" {
		dsn = "file::memory:?cache=shared"
	}
	conn, err := connStringFromDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", conn)
	if err != nil {
		return nil, fmt.Errorf("console: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("console: ping sqlite: %w", err)
	}
	return &DB{db: db}, nil
}

// Close releases the underlying database resources.
func (d *DB) Close() error { return d.db.Close() }

// Ping verifies that the underlying connection is alive.
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Raw returns the underlying *sql.DB. Reserved for tests and migrations.
func (d *DB) Raw() *sql.DB { return d.db }

func connStringFromDSN(dsn string) (string, error) {
	pragma := "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	switch {
	case strings.HasPrefix(dsn, "file:"):
		path := strings.TrimPrefix(dsn, "file:")
		return path + pragma, nil
	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		return path + pragma, nil
	default:
		return "", fmt.Errorf("console: unsupported db dsn scheme: %q", dsn)
	}
}

// Migrate applies the console-local schema. Tables are created idempotently
// so the migration is safe to run on every startup.
func (d *DB) Migrate(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS users (
    subject     TEXT PRIMARY KEY,
    role        TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    display_name TEXT
);

CREATE TABLE IF NOT EXISTS api_tokens (
    token_hash  TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,
    role        TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    last_used_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS instance_preferences (
    subject       TEXT NOT NULL,
    instance_id   TEXT NOT NULL,
    stats_since   TEXT,
    visible_slots INTEGER,
    PRIMARY KEY (subject, instance_id)
);

CREATE TABLE IF NOT EXISTS hidden_station_runs (
    instance_id  TEXT NOT NULL,
    station_id   TEXT NOT NULL,
    task_id      TEXT NOT NULL,
    retry_index  INTEGER NOT NULL,
    hidden_by    TEXT NOT NULL,
    hidden_at    TIMESTAMP NOT NULL,
    PRIMARY KEY (instance_id, station_id, task_id, retry_index)
);

CREATE INDEX IF NOT EXISTS idx_hidden_station_runs_instance_station
    ON hidden_station_runs(instance_id, station_id);

CREATE TABLE IF NOT EXISTS audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TIMESTAMP NOT NULL,
    subject       TEXT NOT NULL,
    instance_id   TEXT,
    action        TEXT NOT NULL,
    station_id    TEXT,
    task_id       TEXT,
    retry_index   INTEGER,
    payload       TEXT,
    status        TEXT NOT NULL,
    message       TEXT
);
`
	for _, stmt := range strings.Split(ddl, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("console: migrate: %w", err)
		}
	}
	return nil
}

// HiddenRunKey identifies a console-local hidden failed run.
type HiddenRunKey struct {
	InstanceID string
	StationID  string
	TaskID     string
	RetryIndex int
}

// HideRun records (idempotently) that a failed run has been acknowledged from
// the dashboard. Repeated calls with the same key succeed without error.
func (d *DB) HideRun(ctx context.Context, k HiddenRunKey, hiddenBy string, now time.Time) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO hidden_station_runs(instance_id, station_id, task_id, retry_index, hidden_by, hidden_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id, station_id, task_id, retry_index) DO UPDATE SET
			hidden_by = excluded.hidden_by,
			hidden_at = excluded.hidden_at
	`, k.InstanceID, k.StationID, k.TaskID, k.RetryIndex, hiddenBy, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("console: hide run: %w", err)
	}
	return nil
}

// HideRuns records several hidden failed runs in one transaction.
func (d *DB) HideRuns(ctx context.Context, keys []HiddenRunKey, hiddenBy string, now time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("console: hide runs: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	hiddenAt := now.UTC().Format(time.RFC3339Nano)
	for _, key := range keys {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hidden_station_runs(instance_id, station_id, task_id, retry_index, hidden_by, hidden_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(instance_id, station_id, task_id, retry_index) DO UPDATE SET
				hidden_by = excluded.hidden_by,
				hidden_at = excluded.hidden_at
		`, key.InstanceID, key.StationID, key.TaskID, key.RetryIndex, hiddenBy, hiddenAt)
		if err != nil {
			return fmt.Errorf("console: hide runs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("console: hide runs commit: %w", err)
	}
	return nil
}

// HiddenForStation returns the set of (task_id, retry_index) pairs hidden for
// a given station.
func (d *DB) HiddenForStation(ctx context.Context, instanceID, stationID string) (map[HiddenRunKey]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT task_id, retry_index FROM hidden_station_runs
		WHERE instance_id = ? AND station_id = ?
	`, instanceID, stationID)
	if err != nil {
		return nil, fmt.Errorf("console: hidden lookup: %w", err)
	}
	defer rows.Close()
	out := map[HiddenRunKey]struct{}{}
	for rows.Next() {
		var taskID string
		var retry int
		if err := rows.Scan(&taskID, &retry); err != nil {
			return nil, err
		}
		out[HiddenRunKey{InstanceID: instanceID, StationID: stationID, TaskID: taskID, RetryIndex: retry}] = struct{}{}
	}
	return out, rows.Err()
}

// UnhideStation removes all hidden-run records for a given station, restoring
// all suppressed slots to the dashboard.
func (d *DB) UnhideStation(ctx context.Context, instanceID, stationID string) error {
	_, err := d.db.ExecContext(ctx, `
		DELETE FROM hidden_station_runs WHERE instance_id = ? AND station_id = ?
	`, instanceID, stationID)
	if err != nil {
		return fmt.Errorf("console: unhide station: %w", err)
	}
	return nil
}

// AuditEntry captures one mutating action executed by the console gateway.
type AuditEntry struct {
	At         time.Time
	Subject    string
	InstanceID string
	Action     string
	StationID  string
	TaskID     string
	RetryIndex *int
	Payload    string
	Status     string
	Message    string
}

// RecordAudit appends an audit-log entry. Errors from this call must not block
// the request because audit logging is a side effect.
func (d *DB) RecordAudit(ctx context.Context, e AuditEntry) error {
	var retry sql.NullInt64
	if e.RetryIndex != nil {
		retry = sql.NullInt64{Int64: int64(*e.RetryIndex), Valid: true}
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO audit_log(at, subject, instance_id, action, station_id, task_id, retry_index, payload, status, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.At.UTC().Format(time.RFC3339Nano), e.Subject, e.InstanceID, e.Action, e.StationID, e.TaskID, retry, e.Payload, e.Status, e.Message)
	if err != nil {
		return fmt.Errorf("console: audit: %w", err)
	}
	return nil
}

// AuditCount returns the number of audit rows recorded. Used by tests.
func (d *DB) AuditCount(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// LookupToken returns the (subject, role) bound to a bearer token hash, or
// ErrUnknownToken if the token is not registered.
func (d *DB) LookupToken(ctx context.Context, hash string) (subject string, role string, err error) {
	row := d.db.QueryRowContext(ctx, `SELECT subject, role FROM api_tokens WHERE token_hash = ?`, hash)
	if err := row.Scan(&subject, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUnknownToken
		}
		return "", "", err
	}
	return subject, role, nil
}

// RegisterToken inserts or updates a console token entry.
func (d *DB) RegisterToken(ctx context.Context, hash, subject, role string, now time.Time) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO api_tokens(token_hash, subject, role, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET subject = excluded.subject, role = excluded.role
	`, hash, subject, role, now.UTC().Format(time.RFC3339Nano))
	return err
}

// ErrUnknownToken is returned when LookupToken cannot find a matching record.
var ErrUnknownToken = errors.New("console: unknown token")
