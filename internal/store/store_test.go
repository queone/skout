package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type testClock struct{ value time.Time }

func (clock testClock) Now() time.Time { return clock.value }

func TestStoreFreshSchemaPragmasPermissionsAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "skout.db")
	database, err := OpenAtWithClock(path, testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := database.SchemaVersion()
	if err != nil || version != CurrentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	wantTables := []string{"command_snapshots", "dashboard_status", "fangraphs_batted_ball", "mlb_game_schedule", "mlb_odds", "mlb_team_active_rosters", "mlbam_season_stats", "player_projections", "players", "schema_version", "season_sync_status", "statcast_seasons", "sync_item_state", "sync_log", "sync_row_state", "sync_runs", "yahoo_free_agents", "yahoo_leagues", "yahoo_roster_positions", "yahoo_roster_slots", "yahoo_stat_categories", "yahoo_teams", "yahoo_transactions"}
	rows, err := database.conn.QueryContext(context.Background(), "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tables, wantTables) {
		t.Fatalf("fresh tables=%v want=%v", tables, wantTables)
	}
	wantIndexes := []string{"idx_mlb_team_active_rosters_mlbam_id", "idx_players_mlbam_id", "idx_yahoo_roster_slots_player_id"}
	indexRows, err := database.conn.QueryContext(context.Background(), "SELECT name FROM sqlite_schema WHERE type='index' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for indexRows.Next() {
		var name string
		if err := indexRows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, name)
	}
	if err := indexRows.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(indexes, wantIndexes) {
		t.Fatalf("fresh indexes=%v want=%v", indexes, wantIndexes)
	}
	var versionRows int64
	if err := database.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_version").Scan(&versionRows); err != nil || versionRows != 1 {
		t.Fatalf("schema version rows=%d err=%v", versionRows, err)
	}
	var journal string
	if err := database.conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal); err != nil || strings.ToLower(journal) != "wal" {
		t.Fatalf("journal=%q err=%v", journal, err)
	}
	var busy int64
	if err := database.conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("busy=%d err=%v", busy, err)
	}
	for target, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode=%o want=%o", target, info.Mode().Perm(), want)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SchemaVersion(); err == nil {
		t.Fatal("query after Close succeeded")
	}
}

func TestProductionOpenOwnsAndReleasesSharedDatabaseGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	path, err := DatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDatabaseGuard(path, DatabaseGuardExclusive); err == nil {
		t.Fatal("exclusive guard acquired while production store remained open")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquireDatabaseGuard(path, DatabaseGuardExclusive)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(); err == nil {
		opened.Close()
		t.Fatal("invalid production database opened")
	}
	guard, err = AcquireDatabaseGuard(path, DatabaseGuardExclusive)
	if err != nil {
		t.Fatalf("failed Open retained guard: %v", err)
	}
	guard.Close()
}

func TestStoreMigratesEverySupportedVersionAndRollsBackInjectedFailures(t *testing.T) {
	for version := int64(1); version < CurrentSchemaVersion; version++ {
		t.Run("Version"+string(rune('0'+version)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, conn := legacyConnection(t, path, version)
			defer db.Close()
			defer conn.Close()
			if err := migrateWithHook(context.Background(), conn, path, nil); err != nil {
				t.Fatalf("migrate version %d: %v", version, err)
			}
			var got int64
			if err := conn.QueryRowContext(context.Background(), "SELECT version FROM schema_version").Scan(&got); err != nil || got != CurrentSchemaVersion {
				t.Fatalf("version=%d err=%v", got, err)
			}
			var retained string
			if err := conn.QueryRowContext(context.Background(), "SELECT value FROM retained").Scan(&retained); err != nil || retained != "keep" {
				t.Fatalf("retained=%q err=%v", retained, err)
			}
		})
	}
	for failureAfter := int64(2); failureAfter <= CurrentSchemaVersion; failureAfter++ {
		path := filepath.Join(t.TempDir(), "rollback.db")
		db, conn := legacyConnection(t, path, 1)
		beforeSchema := schemaSnapshot(t, conn)
		err := migrateWithHook(context.Background(), conn, path, func(version int64) error {
			if version == failureAfter {
				return errors.New("injected")
			}
			return nil
		})
		if err == nil {
			t.Errorf("failure after %d succeeded", failureAfter)
		}
		var version int64
		if scanErr := conn.QueryRowContext(context.Background(), "SELECT version FROM schema_version").Scan(&version); scanErr != nil || version != 1 {
			t.Errorf("failure after %d left version=%d err=%v", failureAfter, version, scanErr)
		}
		var yahooTable int64
		_ = conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_schema WHERE name='yahoo_free_agents'").Scan(&yahooTable)
		if yahooTable != 0 {
			t.Errorf("failure after %d retained transaction objects", failureAfter)
		}
		if afterSchema := schemaSnapshot(t, conn); !slices.Equal(afterSchema, beforeSchema) {
			t.Errorf("failure after %d changed schema\nbefore=%v\nafter=%v", failureAfter, beforeSchema, afterSchema)
		}
		var retained string
		if scanErr := conn.QueryRowContext(context.Background(), "SELECT value FROM retained").Scan(&retained); scanErr != nil || retained != "keep" {
			t.Errorf("failure after %d retained=%q err=%v", failureAfter, retained, scanErr)
		}
		conn.Close()
		db.Close()
	}
}

func TestStoreRejectsUnsupportedSchemaWithoutMutation(t *testing.T) {
	for _, setup := range []string{"CREATE TABLE unrelated(value TEXT); INSERT INTO unrelated VALUES('keep')", "CREATE TABLE schema_version(version INTEGER PRIMARY KEY)", "CREATE TABLE schema_version(version TEXT PRIMARY KEY); INSERT INTO schema_version VALUES('bad')", "CREATE TABLE schema_version(version INTEGER PRIMARY KEY); INSERT INTO schema_version VALUES(0)", "CREATE TABLE schema_version(version INTEGER PRIMARY KEY); INSERT INTO schema_version VALUES(8)", "CREATE TABLE schema_version(version INTEGER PRIMARY KEY); INSERT INTO schema_version VALUES(1); INSERT INTO schema_version VALUES(2)"} {
		path := filepath.Join(t.TempDir(), "invalid.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(setup); err != nil {
			t.Fatal(err)
		}
		db.Close()
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if database, err := OpenAt(path); err == nil {
			database.Close()
			t.Errorf("unsupported setup opened: %s", setup)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("unsupported setup mutated database bytes: %s", setup)
		}
		check, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var journal string
		if err := check.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatal(err)
		}
		check.Close()
		if strings.EqualFold(journal, "wal") {
			t.Errorf("unsupported setup changed journal mode: %s", setup)
		}
	}
}

func TestStoreVersionSixMigrationAddsLeagueArchiveColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-six.db")
	database, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `CREATE TABLE yahoo_leagues_v6 (league_key TEXT PRIMARY KEY, name TEXT NOT NULL, season INTEGER NOT NULL, num_teams INTEGER NOT NULL, scoring_type TEXT NOT NULL, current_week INTEGER, faab_budget INTEGER, max_weekly_adds INTEGER, trade_deadline TEXT, min_ip INTEGER, waiver_type TEXT, synced_at INTEGER NOT NULL);
INSERT INTO yahoo_leagues_v6(league_key,name,season,num_teams,scoring_type,current_week,synced_at) VALUES('mlb.l.9','Kept League',2026,10,'head-to-head',20,1);
DROP TABLE yahoo_leagues; ALTER TABLE yahoo_leagues_v6 RENAME TO yahoo_leagues;
UPDATE schema_version SET version=6`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var name, endDate string
	var finished, archived int64
	if err := migrated.conn.QueryRowContext(context.Background(), "SELECT name,end_date,is_finished,archived FROM yahoo_leagues WHERE league_key='mlb.l.9'").Scan(&name, &endDate, &finished, &archived); err != nil || name != "Kept League" || endDate != "" || finished != 0 || archived != 0 {
		t.Fatalf("league name=%q end_date=%q is_finished=%d archived=%d err=%v", name, endDate, finished, archived, err)
	}
	frozen, err := migrated.LeagueArchived("mlb.l.9")
	if err != nil || frozen {
		t.Fatalf("archived=%v err=%v", frozen, err)
	}
}

func TestStoreRepresentativeVersionThreeAndFiveMigrationsPreserveData(t *testing.T) {
	t.Run("version three", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "version-three.db")
		database, err := OpenAt(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.conn.ExecContext(context.Background(), `UPDATE dashboard_status SET last_error='kept';
ALTER TABLE statcast_seasons RENAME TO statcast_seasons_v6;
CREATE TABLE statcast_seasons(player_id INTEGER NOT NULL,season INTEGER NOT NULL,stat_group TEXT NOT NULL,xwoba REAL,fetched_at INTEGER,PRIMARY KEY(player_id,season,stat_group));
INSERT INTO statcast_seasons(player_id,season,stat_group,xwoba,fetched_at) VALUES(7,2026,'batting',.401,1);
DROP TABLE statcast_seasons_v6;
CREATE TABLE projection_seasons(value TEXT);
INSERT INTO projection_seasons VALUES('remove');
UPDATE schema_version SET version=3`); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		migrated, err := OpenAt(path)
		if err != nil {
			t.Fatal(err)
		}
		defer migrated.Close()
		var xwoba float64
		var strikeout, walk, ops sql.NullFloat64
		if err := migrated.conn.QueryRowContext(context.Background(), "SELECT xwoba,strikeout_pct,walk_pct,ops FROM statcast_seasons WHERE player_id=7").Scan(&xwoba, &strikeout, &walk, &ops); err != nil || xwoba != .401 || strikeout.Valid || walk.Valid || ops.Valid {
			t.Fatalf("statcast row=%v/%v/%v/%v err=%v", xwoba, strikeout, walk, ops, err)
		}
		var lastError string
		if err := migrated.conn.QueryRowContext(context.Background(), "SELECT last_error FROM dashboard_status WHERE id=1").Scan(&lastError); err != nil || lastError != "kept" {
			t.Fatalf("dashboard error=%q err=%v", lastError, err)
		}
		var projectionTables int64
		if err := migrated.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_schema WHERE name='projection_seasons'").Scan(&projectionTables); err != nil || projectionTables != 0 {
			t.Fatalf("projection tables=%d err=%v", projectionTables, err)
		}
	})

	t.Run("version five", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "version-five.db")
		database, err := OpenAt(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO players(mlbam_id,name,synced_at) VALUES(501,'Kept Player',1);
DROP INDEX idx_mlb_team_active_rosters_mlbam_id;
DROP INDEX idx_players_mlbam_id;
DROP INDEX idx_yahoo_roster_slots_player_id;
UPDATE schema_version SET version=5`); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		migrated, err := OpenAt(path)
		if err != nil {
			t.Fatal(err)
		}
		defer migrated.Close()
		var player string
		if err := migrated.conn.QueryRowContext(context.Background(), "SELECT name FROM players WHERE mlbam_id=501").Scan(&player); err != nil || player != "Kept Player" {
			t.Fatalf("player=%q err=%v", player, err)
		}
		for _, name := range []string{"idx_mlb_team_active_rosters_mlbam_id", "idx_players_mlbam_id", "idx_yahoo_roster_slots_player_id"} {
			var count int64
			if err := migrated.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?", name).Scan(&count); err != nil || count != 1 {
				t.Errorf("index %s count=%d err=%v", name, count, err)
			}
		}
	})
}

func legacyConnection(t *testing.T, path string, version int64) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_version(version INTEGER PRIMARY KEY); INSERT INTO schema_version VALUES(?); CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES('keep')", version); err != nil {
		t.Fatal(err)
	}
	if version >= 3 && version <= 4 {
		if _, err := db.Exec(`CREATE TABLE dashboard_status(id INTEGER PRIMARY KEY,last_run_at INTEGER,last_run_status TEXT,next_run_at INTEGER,provider_last_success_at INTEGER,provider_last_failure_at INTEGER,provider_failure_count INTEGER NOT NULL DEFAULT 0,circuit_open INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',provider_freshness_at INTEGER)`); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return db, conn
}

func schemaSnapshot(t *testing.T, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), "SELECT type || ':' || name || ':' || COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
