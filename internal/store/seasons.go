package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SeasonSyncStatus is the completeness state for one source season.
type SeasonSyncStatus string

const (
	SeasonComplete SeasonSyncStatus = "complete"
	SeasonPartial  SeasonSyncStatus = "partial"
	SeasonFailed   SeasonSyncStatus = "failed"
)

// SeasonState contains persisted completeness for one source and season.
type SeasonState struct {
	Source          string
	Season          int64
	Status          SeasonSyncStatus
	FetchedAt       time.Time
	RecordCount     int64
	PipelineVersion int64
}

// SeasonState reads one source-season completeness row.
func (store *Store) SeasonState(source string, season int64) (*SeasonState, error) {
	const operation = "read season state"
	if err := validateIdentity(operation, "source", source); err != nil {
		return nil, err
	}
	row := store.conn.QueryRowContext(context.Background(), `SELECT source,season,status,fetched_at,record_count,pipeline_version
FROM season_sync_status WHERE source=? AND season=?`, source, season)
	var state SeasonState
	var status string
	var fetchedAt int64
	if err := row.Scan(&state.Source, &state.Season, &status, &fetchedAt, &state.RecordCount, &state.PipelineVersion); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	parsed, err := parseSeasonSyncStatus(operation, status)
	if err != nil {
		return nil, err
	}
	state.Status = parsed
	if state.RecordCount < 0 || state.PipelineVersion < 0 {
		return nil, fmt.Errorf("%s: stored counts and pipeline version must not be negative; correct the database and retry", operation)
	}
	if fetchedAt <= 0 {
		return nil, fmt.Errorf("%s: fetched timestamp must be positive; correct the database and retry", operation)
	}
	state.FetchedAt = time.Unix(fetchedAt, 0)
	return &state, nil
}

// IsSeasonComplete reports whether a source season is complete at the requested pipeline version.
func (store *Store) IsSeasonComplete(source string, season, pipelineVersion int64) (bool, error) {
	const operation = "evaluate season completeness"
	if err := validateIdentity(operation, "source", source); err != nil {
		return false, err
	}
	if pipelineVersion < 0 {
		return false, fmt.Errorf("%s: pipeline version must not be negative; correct the value and retry", operation)
	}
	state, err := store.SeasonState(source, season)
	if err != nil {
		return false, err
	}
	return state != nil && state.Status == SeasonComplete && state.PipelineVersion >= pipelineVersion, nil
}

// MarkSeasonComplete marks one source season complete.
func (store *Store) MarkSeasonComplete(source string, season, recordCount, pipelineVersion int64) error {
	return store.writeSeasonState(source, season, SeasonComplete, recordCount, pipelineVersion)
}

// MarkSeasonPartial marks one source season partial.
func (store *Store) MarkSeasonPartial(source string, season, recordCount, pipelineVersion int64) error {
	return store.writeSeasonState(source, season, SeasonPartial, recordCount, pipelineVersion)
}

// MarkSeasonFailed marks one source season failed.
func (store *Store) MarkSeasonFailed(source string, season, recordCount, pipelineVersion int64) error {
	return store.writeSeasonState(source, season, SeasonFailed, recordCount, pipelineVersion)
}

func (store *Store) writeSeasonState(source string, season int64, status SeasonSyncStatus, recordCount, pipelineVersion int64) error {
	const operation = "write season state"
	if err := validateIdentity(operation, "source", source); err != nil {
		return err
	}
	if recordCount < 0 || pipelineVersion < 0 {
		return fmt.Errorf("%s: record count and pipeline version must not be negative; correct the value and retry", operation)
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO season_sync_status(source,season,status,fetched_at,record_count,pipeline_version)
VALUES(?,?,?,?,?,?) ON CONFLICT(source,season) DO UPDATE SET status=excluded.status,fetched_at=excluded.fetched_at,
record_count=excluded.record_count,pipeline_version=excluded.pipeline_version`, source, season, status, now, recordCount, pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

func parseSeasonSyncStatus(operation, value string) (SeasonSyncStatus, error) {
	status := SeasonSyncStatus(value)
	if status != SeasonComplete && status != SeasonPartial && status != SeasonFailed {
		return "", fmt.Errorf("%s: unknown season status %q; correct the database and retry", operation, value)
	}
	return status, nil
}
