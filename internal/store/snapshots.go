package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// CommandSnapshot is the latest successful payload for one command dataset.
type CommandSnapshot struct {
	Dataset          string
	Source           string
	Scope            string
	SnapshotVersion  string
	Payload          string
	LastSuccessfulAt time.Time
	Stale            bool
	ErrorMessage     string
}

// SaveCommandSnapshot atomically replaces one complete JSON snapshot.
func (store *Store) SaveCommandSnapshot(dataset, source, scope, version, payload string) error {
	const operation = "save command snapshot"
	for field, value := range map[string]string{"dataset": dataset, "source": source, "snapshot version": version, "payload": payload} {
		if err := validateIdentity(operation, field, value); err != nil {
			return err
		}
	}
	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("%s: payload is not valid JSON; correct the value and retry", operation)
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		_, err := executor.ExecContext(ctx, `INSERT INTO command_snapshots
(dataset,source,scope,snapshot_version,payload,last_successful_at,stale,error_message)
VALUES(?,?,?,?,?,?,0,'')
ON CONFLICT(dataset,source,scope) DO UPDATE SET
snapshot_version=excluded.snapshot_version,payload=excluded.payload,
last_successful_at=excluded.last_successful_at,stale=0,error_message=''`,
			dataset, source, scope, version, payload, now)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		return nil
	})
}

// CommandSnapshot reads one snapshot including stale metadata.
func (store *Store) CommandSnapshot(dataset, source, scope string) (*CommandSnapshot, error) {
	const operation = "read command snapshot"
	if err := validateIdentity(operation, "dataset", dataset); err != nil {
		return nil, err
	}
	if err := validateIdentity(operation, "source", source); err != nil {
		return nil, err
	}
	row := store.conn.QueryRowContext(context.Background(), `SELECT dataset,source,scope,snapshot_version,payload,last_successful_at,stale,error_message
FROM command_snapshots WHERE dataset=? AND source=? AND scope=?`, dataset, source, scope)
	snapshot, err := scanSnapshot(row)
	if err == nil {
		return snapshot, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return nil, operationError(operation, store.path, err)
}

// CommandSnapshotsByScope reads all sources freshest first.
func (store *Store) CommandSnapshotsByScope(dataset, scope string) ([]CommandSnapshot, error) {
	const operation = "read command snapshots by scope"
	if err := validateIdentity(operation, "dataset", dataset); err != nil {
		return nil, err
	}
	rows, err := store.conn.QueryContext(context.Background(), `SELECT dataset,source,scope,snapshot_version,payload,last_successful_at,stale,error_message
FROM command_snapshots WHERE dataset=? AND scope=? ORDER BY last_successful_at DESC`, dataset, scope)
	if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	defer rows.Close()
	var snapshots []CommandSnapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, operationError(operation, store.path, err)
		}
		snapshots = append(snapshots, *snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError(operation, store.path, err)
	}
	return snapshots, nil
}

// MarkCommandSnapshotStale marks metadata without discarding the payload.
func (store *Store) MarkCommandSnapshotStale(dataset, source, scope, message string) (bool, error) {
	const operation = "mark command snapshot stale"
	if err := validateIdentity(operation, "dataset", dataset); err != nil {
		return false, err
	}
	if err := validateIdentity(operation, "source", source); err != nil {
		return false, err
	}
	if err := validateIdentity(operation, "error message", message); err != nil {
		return false, err
	}
	changed := false
	err := store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		result, err := executor.ExecContext(ctx, "UPDATE command_snapshots SET stale=1,error_message=? WHERE dataset=? AND source=? AND scope=?", message, dataset, source, scope)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		count, err := result.RowsAffected()
		changed = count > 0
		return err
	})
	return changed, err
}

type scanner interface {
	Scan(...any) error
}

func scanSnapshot(row scanner) (*CommandSnapshot, error) {
	var snapshot CommandSnapshot
	var timestamp, stale int64
	if err := row.Scan(&snapshot.Dataset, &snapshot.Source, &snapshot.Scope, &snapshot.SnapshotVersion, &snapshot.Payload, &timestamp, &stale, &snapshot.ErrorMessage); err != nil {
		return nil, err
	}
	if !json.Valid([]byte(snapshot.Payload)) {
		return nil, fmt.Errorf("stored snapshot payload is not valid JSON")
	}
	if timestamp <= 0 {
		return nil, fmt.Errorf("stored snapshot timestamp must be positive")
	}
	if stale != 0 && stale != 1 {
		return nil, fmt.Errorf("stored stale value must be 0 or 1, got %d", stale)
	}
	snapshot.LastSuccessfulAt = time.Unix(timestamp, 0)
	snapshot.Stale = stale == 1
	return &snapshot, nil
}
