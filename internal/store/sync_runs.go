package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// SyncMode identifies a synchronization run class.
type SyncMode string

const (
	SyncLive    SyncMode = "live"
	SyncEvents  SyncMode = "events"
	SyncHistory SyncMode = "history"
)

// SyncOrigin identifies what started a run.
type SyncOrigin string

const (
	OriginManual     SyncOrigin = "manual"
	OriginAutomatic  SyncOrigin = "automatic"
	OriginStartup    SyncOrigin = "startup"
	OriginPublicPull SyncOrigin = "public_pull"
)

// SyncRun is one persisted synchronization lifecycle.
type SyncRun struct {
	ID        int64
	Mode      SyncMode
	Origin    SyncOrigin
	StartedAt time.Time
	EndedAt   *time.Time
	Status    string
	Counts    map[string]int64
}

// StartSyncRun starts one running row.
func (store *Store) StartSyncRun(mode SyncMode, origin SyncOrigin) (int64, error) {
	const operation = "start sync run"
	if !validMode(mode) || !validOrigin(origin) {
		return 0, fmt.Errorf("%s: unknown mode or origin; correct the value and retry", operation)
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return 0, err
	}
	var id int64
	err = store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		result, err := executor.ExecContext(ctx, "INSERT INTO sync_runs(mode,started_at,status,origin,counts) VALUES(?,?,'running',?,NULL)", mode, now, origin)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		id, err = result.LastInsertId()
		return err
	})
	return id, err
}

// CompleteSyncRun completes a running row with deterministic counts.
func (store *Store) CompleteSyncRun(id int64, counts map[string]int64) (bool, error) {
	const operation = "complete sync run"
	if id <= 0 {
		return false, fmt.Errorf("%s: run ID must be positive; correct the value and retry", operation)
	}
	keys := make([]string, 0, len(counts))
	for key, value := range counts {
		if err := validateIdentity(operation, "count key", key); err != nil || value < 0 {
			return false, fmt.Errorf("%s: count keys must be nonempty and values nonnegative; correct the value and retry", operation)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]int64, len(counts))
	for _, key := range keys {
		ordered[key] = counts[key]
	}
	payload, err := json.Marshal(ordered)
	if err != nil {
		return false, err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return false, err
	}
	return store.finishRun(operation, id, now, "complete", string(payload))
}

// FailSyncRun fails a running row without counts.
func (store *Store) FailSyncRun(id int64) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("fail sync run: run ID must be positive; correct the value and retry")
	}
	now, err := store.capturedUnix("fail sync run")
	if err != nil {
		return false, err
	}
	return store.finishRun("fail sync run", id, now, "failed", "")
}

func (store *Store) finishRun(operation string, id, now int64, status, counts string) (bool, error) {
	changed := false
	err := store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		var result sql.Result
		var err error
		if status == "complete" {
			result, err = executor.ExecContext(ctx, "UPDATE sync_runs SET ended_at=?,status='complete',counts=? WHERE id=? AND status='running'", now, counts, id)
		} else {
			result, err = executor.ExecContext(ctx, "UPDATE sync_runs SET ended_at=?,status='failed',counts=NULL WHERE id=? AND status='running'", now, id)
		}
		if err != nil {
			return operationError(operation, store.path, err)
		}
		rows, err := result.RowsAffected()
		changed = rows > 0
		return err
	})
	return changed, err
}

// LatestSyncRun returns the newest row for a mode.
func (store *Store) LatestSyncRun(mode SyncMode) (*SyncRun, error) {
	if !validMode(mode) {
		return nil, fmt.Errorf("read latest sync run: unknown mode; correct the value and retry")
	}
	row := store.conn.QueryRowContext(context.Background(), "SELECT id,mode,started_at,ended_at,status,counts,origin FROM sync_runs WHERE mode=? ORDER BY id DESC LIMIT 1", mode)
	run, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, operationError("read latest sync run", store.path, err)
	}
	return run, nil
}

func scanRun(row scanner) (*SyncRun, error) {
	var run SyncRun
	var started int64
	var ended sql.NullInt64
	var counts sql.NullString
	if err := row.Scan(&run.ID, &run.Mode, &started, &ended, &run.Status, &counts, &run.Origin); err != nil {
		return nil, err
	}
	if run.ID <= 0 || started <= 0 || !validMode(run.Mode) || !validOrigin(run.Origin) {
		return nil, fmt.Errorf("stored sync run is invalid")
	}
	run.StartedAt = time.Unix(started, 0)
	if ended.Valid {
		value := time.Unix(ended.Int64, 0)
		run.EndedAt = &value
	}
	if counts.Valid {
		if err := json.Unmarshal([]byte(counts.String), &run.Counts); err != nil {
			return nil, fmt.Errorf("stored counts are invalid JSON: %w", err)
		}
	}
	return &run, nil
}

func validMode(mode SyncMode) bool {
	return mode == SyncLive || mode == SyncEvents || mode == SyncHistory
}
func validOrigin(origin SyncOrigin) bool {
	return origin == OriginManual || origin == OriginAutomatic || origin == OriginStartup || origin == OriginPublicPull
}
