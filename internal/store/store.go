// Package store owns skout's Rust-compatible SQLite state.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const CurrentSchemaVersion int64 = 6

//go:embed schema.sql
var schemaSQL string

// Clock supplies wall time to durable state transitions.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Store owns one dedicated SQLite connection.
type Store struct {
	db    *sql.DB
	conn  *sql.Conn
	path  string
	clock Clock
	guard *DatabaseGuard
}

// DatabasePath resolves the production database path without creating it.
func DatabasePath() (string, error) {
	home, ok := os.LookupEnv("HOME")
	if !ok || home == "" {
		return "", fmt.Errorf("resolve database path: HOME is unavailable; set HOME to the user home directory and retry")
	}
	return filepath.Join(home, ".config", "skout", "skout.db"), nil
}

// Open opens and migrates the production database.
func Open() (*Store, error) {
	path, err := DatabasePath()
	if err != nil {
		return nil, err
	}
	guard, err := AcquireDatabaseGuard(path, DatabaseGuardShared)
	if err != nil {
		return nil, fmt.Errorf("open database: acquire operation guard: %w", err)
	}
	database, err := OpenAt(path)
	if err != nil {
		return nil, errors.Join(err, guard.Close())
	}
	database.guard = guard
	return database, nil
}

// OpenAt opens and migrates an explicit database.
func OpenAt(path string) (*Store, error) { return OpenAtWithClock(path, systemClock{}) }

// OpenAtWithClock opens an explicit database with controlled time.
func OpenAtWithClock(path string, clock Clock) (*Store, error) {
	if err := preparePath(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, operationError("open database", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, operationError("own database connection", path, err)
	}
	store := &Store{db: db, conn: conn, path: path, clock: clock}
	closeOnError := func(err error) (*Store, error) {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return closeOnError(operationError("set busy timeout", path, err))
	}
	if err := preflightMigrationSource(ctx, conn, path); err != nil {
		return closeOnError(err)
	}
	var journal string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return closeOnError(operationError("enable WAL journal mode", path, err))
	}
	if !strings.EqualFold(journal, "wal") {
		return closeOnError(operationError("enable WAL journal mode", path, fmt.Errorf("SQLite returned %q", journal)))
	}
	if err := migrate(ctx, conn, path); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

func preflightMigrationSource(ctx context.Context, conn *sql.Conn, path string) error {
	var userTables int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return operationError("inspect schema tables", path, err)
	}
	var versionTables int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='schema_version'").Scan(&versionTables); err != nil {
		return operationError("inspect schema version table", path, err)
	}
	if versionTables == 0 {
		if userTables != 0 {
			return unsupported(path, "nonempty database has no schema_version table")
		}
		return nil
	}
	var versionRows int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version").Scan(&versionRows); err != nil {
		return operationError("count schema version rows", path, err)
	}
	if versionRows != 1 {
		return unsupported(path, fmt.Sprintf("schema_version contains %d rows; expected exactly one", versionRows))
	}
	var version int64
	if err := conn.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		return operationError("read schema version row", path, err)
	}
	if version < 1 || version > CurrentSchemaVersion {
		return unsupported(path, fmt.Sprintf("database schema version %d is not a supported skout migration source", version))
	}
	return nil
}

// Close explicitly releases the dedicated connection and pool.
func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	var failures []error
	if store.conn != nil {
		if err := store.conn.Close(); err != nil {
			failures = append(failures, operationError("close database connection", store.path, err))
		}
		store.conn = nil
	}
	if store.db != nil {
		if err := store.db.Close(); err != nil {
			failures = append(failures, operationError("close database", store.path, err))
		}
		store.db = nil
	}
	if store.guard != nil {
		if err := store.guard.Close(); err != nil {
			failures = append(failures, fmt.Errorf("close database operation guard: %w", err))
		}
		store.guard = nil
	}
	return errors.Join(failures...)
}

// Path returns the database path.
func (store *Store) Path() string { return store.path }

// SchemaVersion returns the one current schema version.
func (store *Store) SchemaVersion() (int64, error) {
	if store == nil || store.conn == nil {
		return 0, fmt.Errorf("read schema version: store is closed; reopen the database and retry")
	}
	var version int64
	err := store.conn.QueryRowContext(context.Background(), "SELECT version FROM schema_version").Scan(&version)
	if err != nil {
		return 0, operationError("read schema version", store.path, err)
	}
	return version, nil
}

// IsEmpty reports whether no synchronized Yahoo league exists.
func (store *Store) IsEmpty() (bool, error) {
	var empty bool
	err := store.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) = 0 FROM yahoo_leagues").Scan(&empty)
	if err != nil {
		return false, operationError("check database emptiness", store.path, err)
	}
	return empty, nil
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (store *Store) immediate(operation string, body func(context.Context, sqlExecutor) error) error {
	ctx := context.Background()
	if _, err := store.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return operationError("begin "+operation, store.path, err)
	}
	if err := body(ctx, store.conn); err != nil {
		if _, rollbackErr := store.conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			return fmt.Errorf("%s and rollback failed (%v): %w", err, rollbackErr, err)
		}
		return err
	}
	if _, err := store.conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = store.conn.ExecContext(ctx, "ROLLBACK")
		return operationError("commit "+operation, store.path, err)
	}
	return nil
}

func (store *Store) capturedUnix(operation string) (int64, error) {
	now := store.clock.Now()
	if now.Before(time.Unix(1, 0)) {
		return 0, fmt.Errorf("%s: clock must be after the Unix epoch; correct the clock and retry", operation)
	}
	return now.Unix(), nil
}

func preparePath(path string) error {
	if path == "" {
		return fmt.Errorf("open database: path is empty; correct the value and retry")
	}
	parent := filepath.Dir(path)
	if parent != "." && parent != "" {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return operationError("create database directory", path, err)
		}
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return file.Close()
	}
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	return operationError("create database file", path, err)
}

func migrate(ctx context.Context, conn *sql.Conn, path string) error {
	return migrateWithHook(ctx, conn, path, nil)
}

func migrateWithHook(ctx context.Context, conn *sql.Conn, path string, afterStep func(int64) error) error {
	var userTables int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return operationError("inspect schema tables", path, err)
	}
	var versionTables int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='schema_version'").Scan(&versionTables); err != nil {
		return operationError("inspect schema version table", path, err)
	}
	if versionTables == 0 {
		if userTables != 0 {
			return unsupported(path, "nonempty database has no schema_version table")
		}
		return runMigration(ctx, conn, path, func(executor sqlExecutor) error {
			if _, err := executor.ExecContext(ctx, schemaSQL); err != nil {
				return operationError("apply schema migration", path, err)
			}
			if _, err := executor.ExecContext(ctx, "INSERT OR IGNORE INTO dashboard_status (id) VALUES (1)"); err != nil {
				return operationError("initialize dashboard status", path, err)
			}
			if _, err := executor.ExecContext(ctx, "DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES (?)", CurrentSchemaVersion); err != nil {
				return operationError("write schema version", path, err)
			}
			return nil
		})
	}
	var versionRows int64
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version").Scan(&versionRows); err != nil {
		return operationError("count schema version rows", path, err)
	}
	if versionRows != 1 {
		return unsupported(path, fmt.Sprintf("schema_version contains %d rows; expected exactly one", versionRows))
	}
	var version int64
	if err := conn.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		return operationError("read schema version row", path, err)
	}
	if version == CurrentSchemaVersion {
		return nil
	}
	if version < 1 || version > CurrentSchemaVersion {
		return unsupported(path, fmt.Sprintf("database schema version %d is not a supported skout migration source", version))
	}
	return runMigration(ctx, conn, path, func(executor sqlExecutor) error {
		for current := version; current < CurrentSchemaVersion; current++ {
			if err := migrateStep(ctx, executor, path, current); err != nil {
				return err
			}
			if afterStep != nil {
				if err := afterStep(current + 1); err != nil {
					return fmt.Errorf("inject migration failure after version %d: %w", current+1, err)
				}
			}
		}
		return nil
	})
}

func runMigration(ctx context.Context, conn *sql.Conn, path string, body func(sqlExecutor) error) error {
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return operationError("begin schema migration", path, err)
	}
	if err := body(conn); err != nil {
		if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			return fmt.Errorf("%s and schema rollback failed (%v): %w", err, rollbackErr, err)
		}
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return operationError("commit schema migration", path, err)
	}
	return nil
}

func migrateStep(ctx context.Context, executor sqlExecutor, path string, version int64) error {
	var script string
	switch version {
	case 1:
		script = "CREATE TABLE yahoo_free_agents (league_key TEXT NOT NULL, player_id INTEGER NOT NULL, synced_at INTEGER NOT NULL, PRIMARY KEY (league_key, player_id)); UPDATE schema_version SET version=2"
	case 2:
		script = `CREATE TABLE dashboard_status (
id INTEGER PRIMARY KEY CHECK (id = 1), daemon_started_at INTEGER, daemon_stopped_at INTEGER,
last_run_at INTEGER, last_run_status TEXT, next_run_at INTEGER,
provider_last_success_at INTEGER, provider_last_failure_at INTEGER,
provider_failure_count INTEGER NOT NULL DEFAULT 0, circuit_open INTEGER NOT NULL DEFAULT 0,
last_error TEXT NOT NULL DEFAULT '', provider_freshness_at INTEGER);
INSERT INTO dashboard_status(id) VALUES(1); UPDATE schema_version SET version=3`
	case 3:
		exists, err := tableExists(ctx, executor, "statcast_seasons")
		if err != nil {
			return operationError("inspect version-four migration", path, err)
		}
		if exists {
			script = "ALTER TABLE statcast_seasons ADD COLUMN strikeout_pct REAL; ALTER TABLE statcast_seasons ADD COLUMN walk_pct REAL; ALTER TABLE statcast_seasons ADD COLUMN ops REAL;"
		}
		script += " UPDATE schema_version SET version=4"
	case 4:
		script = `CREATE TABLE dashboard_status_v5 (
id INTEGER PRIMARY KEY CHECK (id = 1), last_run_at INTEGER, last_run_status TEXT,
next_run_at INTEGER, provider_last_success_at INTEGER, provider_last_failure_at INTEGER,
provider_failure_count INTEGER NOT NULL DEFAULT 0, circuit_open INTEGER NOT NULL DEFAULT 0,
last_error TEXT NOT NULL DEFAULT '', provider_freshness_at INTEGER);
INSERT INTO dashboard_status_v5 SELECT id,last_run_at,last_run_status,next_run_at,
provider_last_success_at,provider_last_failure_at,provider_failure_count,circuit_open,last_error,
provider_freshness_at FROM dashboard_status;
DROP TABLE dashboard_status; ALTER TABLE dashboard_status_v5 RENAME TO dashboard_status;
DROP TABLE IF EXISTS projection_seasons;
CREATE TABLE IF NOT EXISTS player_projections (player_id INTEGER NOT NULL, season INTEGER NOT NULL, source TEXT NOT NULL, stat_group TEXT NOT NULL, pa REAL, ip REAL, hr REAL, r REAL, rbi REAL, sb REAL, avg REAL, obp REAL, slg REAL, era REAL, whip REAL, k REAL, w REAL, sv REAL, bb REAL, fetched_at INTEGER NOT NULL, PRIMARY KEY(player_id,season,source,stat_group));
CREATE TABLE IF NOT EXISTS fangraphs_batted_ball (player_id INTEGER NOT NULL, season INTEGER NOT NULL, fb_pct REAL, hr_fb_pct REAL, fetched_at INTEGER NOT NULL, PRIMARY KEY(player_id,season));
UPDATE schema_version SET version=5`
	case 5:
		indexes := []struct{ table, sql string }{
			{"mlb_team_active_rosters", "CREATE INDEX IF NOT EXISTS idx_mlb_team_active_rosters_mlbam_id ON mlb_team_active_rosters(mlbam_id)"},
			{"players", "CREATE INDEX IF NOT EXISTS idx_players_mlbam_id ON players(mlbam_id)"},
			{"yahoo_roster_slots", "CREATE INDEX IF NOT EXISTS idx_yahoo_roster_slots_player_id ON yahoo_roster_slots(player_id)"},
		}
		for _, index := range indexes {
			exists, err := tableExists(ctx, executor, index.table)
			if err != nil {
				return operationError("check version-six migration target table", path, err)
			}
			if exists {
				if _, err := executor.ExecContext(ctx, index.sql); err != nil {
					return operationError("apply version-six schema migration", path, err)
				}
			}
		}
		script = "UPDATE schema_version SET version=6"
	default:
		return unsupported(path, fmt.Sprintf("database schema version %d is not supported", version))
	}
	if _, err := executor.ExecContext(ctx, script); err != nil {
		return operationError(fmt.Sprintf("apply version-%d schema migration", version+1), path, err)
	}
	return nil
}

func tableExists(ctx context.Context, executor sqlExecutor, name string) (bool, error) {
	var count int64
	err := executor.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?", name).Scan(&count)
	return count != 0, err
}

func readOnlyDSN(path string) string {
	target := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	return target.String()
}

func validateIdentity(operation, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: %s must not be empty; correct the value and retry", operation, field)
	}
	return nil
}

func operationError(operation, path string, err error) error {
	return fmt.Errorf("%s %s: %w; check the path and permissions, then retry", operation, path, err)
}

func unsupported(path, detail string) error {
	return fmt.Errorf("inspect schema %s: %s; move the database aside or import it through a supported migration", path, detail)
}
