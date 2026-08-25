package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const freshnessDetailLimit = 256

// SyncStateStatus is the persisted state of one provider refresh.
type SyncStateStatus string

const (
	SyncStateRunning  SyncStateStatus = "running"
	SyncStateComplete SyncStateStatus = "complete"
	SyncStateFailed   SyncStateStatus = "failed"
)

// SyncItemState contains persisted freshness for one provider item and scope.
type SyncItemState struct {
	Source           string
	Item             string
	Scope            string
	LastAttemptedAt  *time.Time
	LastSuccessfulAt *time.Time
	Status           SyncStateStatus
	ErrorMessage     string
	PipelineVersion  string
}

// ItemRefreshPolicy decides whether one provider item must refresh.
type ItemRefreshPolicy struct {
	TTL             time.Duration
	Force           bool
	PipelineVersion string
}

// SyncRowState contains persisted freshness for one normalized provider row.
type SyncRowState struct {
	Source           string
	Item             string
	Scope            string
	EntityKind       string
	EntityKey        string
	LocalID          *int64
	LastAttemptedAt  *time.Time
	LastSuccessfulAt *time.Time
	Status           SyncStateStatus
	ErrorMessage     string
	PipelineVersion  string
}

// RowRefreshPolicy decides whether one normalized provider row must refresh.
type RowRefreshPolicy struct {
	TTL             time.Duration
	Force           bool
	PipelineVersion string
}

// SyncItemState reads one item freshness row.
func (store *Store) SyncItemState(source, item, scope string) (*SyncItemState, error) {
	const operation = "read item freshness"
	if err := validateItemIdentity(operation, source, item); err != nil {
		return nil, err
	}
	row := store.conn.QueryRowContext(context.Background(), `SELECT source,item,scope,last_attempted_at,last_successful_at,status,error_message,pipeline_version
FROM sync_item_state WHERE source=? AND item=? AND scope=?`, source, item, scope)
	var state SyncItemState
	var attempted, successful int64
	var status string
	if err := row.Scan(&state.Source, &state.Item, &state.Scope, &attempted, &successful, &status, &state.ErrorMessage, &state.PipelineVersion); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	parsed, err := parseSyncStateStatus(operation, status)
	if err != nil {
		return nil, err
	}
	state.Status = parsed
	if state.LastAttemptedAt, err = optionalTimestamp(operation, "last attempted timestamp", attempted); err != nil {
		return nil, err
	}
	if state.LastSuccessfulAt, err = optionalTimestamp(operation, "last successful timestamp", successful); err != nil {
		return nil, err
	}
	return &state, nil
}

// NeedsSyncItem reports whether one item must refresh under the supplied policy.
func (store *Store) NeedsSyncItem(source, item, scope string, policy ItemRefreshPolicy) (bool, error) {
	const operation = "evaluate item freshness"
	if err := validateItemIdentity(operation, source, item); err != nil {
		return false, err
	}
	if err := validateIdentity(operation, "pipeline version", policy.PipelineVersion); err != nil {
		return false, err
	}
	if policy.TTL < 0 {
		return false, fmt.Errorf("%s: TTL must not be negative; correct the value and retry", operation)
	}
	if policy.Force {
		return true, nil
	}
	state, err := store.SyncItemState(source, item, scope)
	if err != nil {
		return false, err
	}
	if state == nil || state.Status != SyncStateComplete || state.PipelineVersion != policy.PipelineVersion || state.LastSuccessfulAt == nil {
		return true, nil
	}
	now, err := store.capturedTime(operation)
	if err != nil {
		return false, err
	}
	age := max(now.Sub(*state.LastSuccessfulAt), 0)
	return age > policy.TTL, nil
}

// MarkSyncItemAttempt records an item refresh attempt without advancing prior success.
func (store *Store) MarkSyncItemAttempt(source, item, scope, pipelineVersion string) error {
	const operation = "record item refresh attempt"
	if err := validateItemWrite(operation, source, item, pipelineVersion); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_item_state(source,item,scope,last_attempted_at,status,pipeline_version)
VALUES(?,?,?,?,'running',?) ON CONFLICT(source,item,scope) DO UPDATE SET
last_attempted_at=excluded.last_attempted_at,status=excluded.status,pipeline_version=excluded.pipeline_version`, source, item, scope, now, pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// MarkSyncItemSuccess records a successful item refresh.
func (store *Store) MarkSyncItemSuccess(source, item, scope, pipelineVersion string) error {
	return store.markSyncItemComplete("record item refresh success", source, item, scope, pipelineVersion, "")
}

// MarkSyncItemDegraded records a complete refresh with issue detail.
func (store *Store) MarkSyncItemDegraded(source, item, scope, pipelineVersion, detail string) error {
	const operation = "record degraded item refresh"
	if err := validateIdentity(operation, "issue detail", detail); err != nil {
		return err
	}
	return store.markSyncItemComplete(operation, source, item, scope, pipelineVersion, boundFreshnessDetail(detail))
}

func (store *Store) markSyncItemComplete(operation, source, item, scope, pipelineVersion, detail string) error {
	if err := validateItemWrite(operation, source, item, pipelineVersion); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_item_state(source,item,scope,last_attempted_at,last_successful_at,status,error_message,pipeline_version)
VALUES(?,?,?,?,?,'complete',?,?) ON CONFLICT(source,item,scope) DO UPDATE SET
last_attempted_at=excluded.last_attempted_at,last_successful_at=excluded.last_successful_at,status=excluded.status,
error_message=excluded.error_message,pipeline_version=excluded.pipeline_version`, source, item, scope, now, now, detail, pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// MarkSyncItemFailure records a failed refresh while retaining prior success.
func (store *Store) MarkSyncItemFailure(source, item, scope, pipelineVersion, message string) error {
	const operation = "record item refresh failure"
	if err := validateItemWrite(operation, source, item, pipelineVersion); err != nil {
		return err
	}
	if err := validateIdentity(operation, "error message", message); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_item_state(source,item,scope,last_attempted_at,status,error_message,pipeline_version)
VALUES(?,?,?,?,'failed',?,?) ON CONFLICT(source,item,scope) DO UPDATE SET
last_attempted_at=excluded.last_attempted_at,status=excluded.status,error_message=excluded.error_message,
pipeline_version=excluded.pipeline_version`, source, item, scope, now, boundFreshnessDetail(message), pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// SyncRowState reads one row freshness record.
func (store *Store) SyncRowState(source, item, scope, entityKind, entityKey string) (*SyncRowState, error) {
	const operation = "read row freshness"
	if err := validateRowIdentity(operation, source, item, entityKind, entityKey); err != nil {
		return nil, err
	}
	row := store.conn.QueryRowContext(context.Background(), `SELECT source,item,scope,entity_kind,entity_key,local_id,last_attempted_at,last_successful_at,status,error_message,pipeline_version
FROM sync_row_state WHERE source=? AND item=? AND scope=? AND entity_kind=? AND entity_key=?`, source, item, scope, entityKind, entityKey)
	var state SyncRowState
	var localID sql.NullInt64
	var attempted, successful int64
	var status string
	if err := row.Scan(&state.Source, &state.Item, &state.Scope, &state.EntityKind, &state.EntityKey, &localID, &attempted, &successful, &status, &state.ErrorMessage, &state.PipelineVersion); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	if localID.Valid {
		if localID.Int64 <= 0 {
			return nil, fmt.Errorf("%s: stored local ID must be positive; correct the database and retry", operation)
		}
		value := localID.Int64
		state.LocalID = &value
	}
	parsed, err := parseSyncStateStatus(operation, status)
	if err != nil {
		return nil, err
	}
	state.Status = parsed
	if state.LastAttemptedAt, err = optionalTimestamp(operation, "last attempted timestamp", attempted); err != nil {
		return nil, err
	}
	if state.LastSuccessfulAt, err = optionalTimestamp(operation, "last successful timestamp", successful); err != nil {
		return nil, err
	}
	return &state, nil
}

// NeedsSyncRow reports whether one normalized row must refresh.
func (store *Store) NeedsSyncRow(source, item, scope, entityKind, entityKey string, policy RowRefreshPolicy) (bool, error) {
	const operation = "evaluate row freshness"
	if err := validateRowIdentity(operation, source, item, entityKind, entityKey); err != nil {
		return false, err
	}
	if err := validateIdentity(operation, "pipeline version", policy.PipelineVersion); err != nil {
		return false, err
	}
	if policy.TTL < 0 {
		return false, fmt.Errorf("%s: TTL must not be negative; correct the value and retry", operation)
	}
	if policy.Force {
		return true, nil
	}
	state, err := store.SyncRowState(source, item, scope, entityKind, entityKey)
	if err != nil {
		return false, err
	}
	if state == nil || state.Status != SyncStateComplete || state.PipelineVersion != policy.PipelineVersion || state.LastSuccessfulAt == nil {
		return true, nil
	}
	now, err := store.capturedTime(operation)
	if err != nil {
		return false, err
	}
	age := max(now.Sub(*state.LastSuccessfulAt), 0)
	return age > policy.TTL, nil
}

// MarkSyncRowAttempt records a row refresh attempt without advancing prior success.
func (store *Store) MarkSyncRowAttempt(source, item, scope, entityKind, entityKey string, localID *int64, pipelineVersion string) error {
	const operation = "record row refresh attempt"
	if err := validateRowWrite(operation, source, item, entityKind, entityKey, localID, pipelineVersion); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_row_state(source,item,scope,entity_kind,entity_key,local_id,last_attempted_at,status,pipeline_version)
VALUES(?,?,?,?,?,?,?,'running',?) ON CONFLICT(source,item,scope,entity_kind,entity_key) DO UPDATE SET
local_id=excluded.local_id,last_attempted_at=excluded.last_attempted_at,status=excluded.status,
pipeline_version=excluded.pipeline_version`, source, item, scope, entityKind, entityKey, localID, now, pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// MarkSyncRowSuccess records successful freshness for one source row.
func (store *Store) MarkSyncRowSuccess(source, item, scope, entityKind, entityKey string, localID *int64, pipelineVersion string) error {
	return store.markSyncRowComplete("record row refresh success", source, item, scope, entityKind, entityKey, localID, pipelineVersion, "")
}

// MarkSyncRowDegraded records complete row freshness with issue detail.
func (store *Store) MarkSyncRowDegraded(source, item, scope, entityKind, entityKey string, localID *int64, pipelineVersion, detail string) error {
	const operation = "record degraded row refresh"
	if err := validateIdentity(operation, "issue detail", detail); err != nil {
		return err
	}
	return store.markSyncRowComplete(operation, source, item, scope, entityKind, entityKey, localID, pipelineVersion, boundFreshnessDetail(detail))
}

func (store *Store) markSyncRowComplete(operation, source, item, scope, entityKind, entityKey string, localID *int64, pipelineVersion, detail string) error {
	if err := validateRowWrite(operation, source, item, entityKind, entityKey, localID, pipelineVersion); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_row_state(source,item,scope,entity_kind,entity_key,local_id,last_attempted_at,last_successful_at,status,error_message,pipeline_version)
VALUES(?,?,?,?,?,?,?,?,'complete',?,?) ON CONFLICT(source,item,scope,entity_kind,entity_key) DO UPDATE SET
local_id=excluded.local_id,last_attempted_at=excluded.last_attempted_at,last_successful_at=excluded.last_successful_at,
status=excluded.status,error_message=excluded.error_message,pipeline_version=excluded.pipeline_version`, source, item, scope, entityKind, entityKey, localID, now, now, detail, pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// MarkSyncRowFailure records failed freshness while retaining prior success.
func (store *Store) MarkSyncRowFailure(source, item, scope, entityKind, entityKey string, localID *int64, pipelineVersion, message string) error {
	const operation = "record row refresh failure"
	if err := validateRowWrite(operation, source, item, entityKind, entityKey, localID, pipelineVersion); err != nil {
		return err
	}
	if err := validateIdentity(operation, "error message", message); err != nil {
		return err
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO sync_row_state(source,item,scope,entity_kind,entity_key,local_id,last_attempted_at,status,error_message,pipeline_version)
VALUES(?,?,?,?,?,?,?,'failed',?,?) ON CONFLICT(source,item,scope,entity_kind,entity_key) DO UPDATE SET
local_id=excluded.local_id,last_attempted_at=excluded.last_attempted_at,status=excluded.status,
error_message=excluded.error_message,pipeline_version=excluded.pipeline_version`, source, item, scope, entityKind, entityKey, localID, now, boundFreshnessDetail(message), pipelineVersion)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

func validateItemIdentity(operation, source, item string) error {
	if err := validateIdentity(operation, "source", source); err != nil {
		return err
	}
	return validateIdentity(operation, "item", item)
}

func validateItemWrite(operation, source, item, pipelineVersion string) error {
	if err := validateItemIdentity(operation, source, item); err != nil {
		return err
	}
	return validateIdentity(operation, "pipeline version", pipelineVersion)
}

func validateRowIdentity(operation, source, item, entityKind, entityKey string) error {
	if err := validateItemIdentity(operation, source, item); err != nil {
		return err
	}
	if err := validateIdentity(operation, "entity kind", entityKind); err != nil {
		return err
	}
	return validateIdentity(operation, "entity key", entityKey)
}

func validateRowWrite(operation, source, item, entityKind, entityKey string, localID *int64, pipelineVersion string) error {
	if err := validateRowIdentity(operation, source, item, entityKind, entityKey); err != nil {
		return err
	}
	if localID != nil && *localID <= 0 {
		return fmt.Errorf("%s: local ID must be positive when present; correct the value and retry", operation)
	}
	return validateIdentity(operation, "pipeline version", pipelineVersion)
}

func parseSyncStateStatus(operation, value string) (SyncStateStatus, error) {
	status := SyncStateStatus(value)
	if status != SyncStateRunning && status != SyncStateComplete && status != SyncStateFailed {
		return "", fmt.Errorf("%s: unknown sync status %q; correct the database and retry", operation, value)
	}
	return status, nil
}

func optionalTimestamp(operation, field string, value int64) (*time.Time, error) {
	if value < 0 {
		return nil, fmt.Errorf("%s: %s must not precede the Unix epoch; correct the database and retry", operation, field)
	}
	if value == 0 {
		return nil, nil
	}
	parsed := time.Unix(value, 0)
	return &parsed, nil
}

func boundFreshnessDetail(value string) string {
	runes := []rune(value)
	if len(runes) <= freshnessDetailLimit {
		return value
	}
	return string(runes[:freshnessDetailLimit])
}

func (store *Store) capturedTime(operation string) (time.Time, error) {
	now := store.clock.Now()
	if now.Before(time.Unix(1, 0)) {
		return time.Time{}, fmt.Errorf("%s: clock must be after the Unix epoch; correct the clock and retry", operation)
	}
	return now, nil
}
