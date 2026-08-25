package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
)

// Status contains the read-only local fields rendered by skout st.
type Status struct {
	LatestSyncStatus     *string
	LatestSyncAt         *int64
	LeagueSyncedAt       *int64
	CircuitOpen          bool
	ProviderFailureCount int64
	ProviderLastError    *string
	ProviderFreshnessAt  *int64
	LastRunAt            *int64
	LastRunStatus        *string
	SchemaVersion        *int64
	DatabaseBytes        *int64
	MLBIdentityCount     int64
	YahooIdentityCount   int64
	UnmatchedPlayerCount int64
	FangraphsSync        *string
	FantasyProsSync      *string
	SavantBBEUnavailable bool
}

// InspectStatusAt inspects an existing database without creating or migrating it.
func InspectStatusAt(path, leagueKey string) (Status, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, operationError("inspect database status", path, err)
	}
	if !info.Mode().IsRegular() {
		return Status{}, nil
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return Status{}, operationError("open database status", path, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return Status{}, operationError("own database status connection", path, err)
	}
	defer conn.Close()
	status := Status{}
	bytes := info.Size()
	status.DatabaseBytes = &bytes

	has := func(table string) (bool, error) { return tableExists(ctx, conn, table) }
	if exists, err := has("schema_version"); err != nil {
		return Status{}, operationError("check schema version table", path, err)
	} else if exists {
		var version int64
		if err := conn.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err == nil {
			status.SchemaVersion = &version
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Status{}, operationError("read schema version", path, err)
		}
	}
	if exists, err := has("sync_runs"); err != nil {
		return Status{}, operationError("check sync runs", path, err)
	} else if exists {
		var runStatus string
		var runAt int64
		err := conn.QueryRowContext(ctx, "SELECT status,COALESCE(ended_at,started_at) FROM sync_runs WHERE mode='live' ORDER BY id DESC LIMIT 1").Scan(&runStatus, &runAt)
		if err == nil {
			status.LatestSyncStatus = &runStatus
			status.LatestSyncAt = &runAt
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Status{}, operationError("read latest sync status", path, err)
		}
	}
	if leagueKey != "" {
		if exists, err := has("yahoo_leagues"); err != nil {
			return Status{}, operationError("check Yahoo leagues", path, err)
		} else if exists {
			var synced int64
			err := conn.QueryRowContext(ctx, "SELECT synced_at FROM yahoo_leagues WHERE league_key=?", leagueKey).Scan(&synced)
			if err == nil {
				status.LeagueSyncedAt = &synced
			} else if !errors.Is(err, sql.ErrNoRows) {
				return Status{}, operationError("read league freshness", path, err)
			}
		}
	}
	if exists, err := has("dashboard_status"); err != nil {
		return Status{}, operationError("check dashboard status table", path, err)
	} else if exists {
		var circuit int64
		var lastError string
		var freshness, lastRun sql.NullInt64
		var lastStatus sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT provider_failure_count,circuit_open,last_error,provider_freshness_at,last_run_at,last_run_status FROM dashboard_status WHERE id=1").Scan(
			&status.ProviderFailureCount, &circuit, &lastError, &freshness, &lastRun, &lastStatus,
		)
		if err == nil {
			status.CircuitOpen = circuit != 0
			if lastError != "" {
				status.ProviderLastError = &lastError
			}
			if freshness.Valid {
				value := freshness.Int64
				status.ProviderFreshnessAt = &value
			}
			if lastRun.Valid {
				value := lastRun.Int64
				status.LastRunAt = &value
			}
			if lastStatus.Valid && lastStatus.String != "" {
				value := lastStatus.String
				status.LastRunStatus = &value
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Status{}, operationError("read dashboard status", path, err)
		}
	}
	if exists, err := has("players"); err != nil {
		return Status{}, operationError("check players table", path, err)
	} else if exists {
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE mlbam_id IS NOT NULL),COUNT(*) FILTER (WHERE yahoo_player_id IS NOT NULL),COUNT(*) FILTER (WHERE mlbam_id IS NULL) FROM players").Scan(
			&status.MLBIdentityCount, &status.YahooIdentityCount, &status.UnmatchedPlayerCount,
		); err != nil {
			return Status{}, operationError("count player identities", path, err)
		}
	}
	providerState := func(source string) (*string, error) {
		var value string
		err := conn.QueryRowContext(ctx, "SELECT status || CASE WHEN last_successful_at IS NULL THEN '' ELSE ' at unix ' || last_successful_at END || CASE WHEN error_message='' THEN '' ELSE ': ' || error_message END FROM sync_item_state WHERE source=? ORDER BY last_attempted_at DESC LIMIT 1", source).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	if exists, err := has("sync_item_state"); err != nil {
		return Status{}, operationError("check provider state", path, err)
	} else if exists {
		status.FangraphsSync, err = providerState("fangraphs")
		if err != nil {
			return Status{}, operationError("read FanGraphs status", path, err)
		}
		status.FantasyProsSync, err = providerState("fantasypros")
		if err != nil {
			return Status{}, operationError("read FantasyPros status", path, err)
		}
	}
	if exists, err := has("statcast_seasons"); err != nil {
		return Status{}, operationError("check Statcast table", path, err)
	} else if exists {
		var rows, withExitVelocity int64
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(exit_velo_avg) FROM statcast_seasons WHERE stat_group='batting' AND season=(SELECT MAX(season) FROM statcast_seasons WHERE stat_group='batting')").Scan(&rows, &withExitVelocity); err != nil {
			return Status{}, operationError("read Savant BBE coverage", path, err)
		}
		status.SavantBBEUnavailable = rows > 0 && withExitVelocity == 0
	}
	return status, nil
}
