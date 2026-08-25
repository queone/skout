package store

import (
	"context"
	"database/sql"
	"fmt"
)

// StatcastWrite is one normalized Baseball Savant season row keyed by MLB identity.
type StatcastWrite struct {
	MLBAMID           int64
	Season            int64
	StatGroup         string
	PlateAppearances  int64
	BattedBallEvents  int64
	XWOBA             *float64
	ExitVeloAverage   *float64
	BarrelPercent     *float64
	HardHitPercent    *float64
	SprintSpeed       *float64
	StrikeoutPercent  *float64
	WalkPercent       *float64
	OPS               *float64
	FastballVelo      *float64
	WhiffPercent      *float64
	ChasePercent      *float64
	GroundBallPercent *float64
}

// ReplaceStatcastSnapshot atomically replaces one complete Savant season and group.
func (store *Store) ReplaceStatcastSnapshot(season int64, group string, rows []StatcastWrite) (int, error) {
	return store.replaceStatcastSnapshot(season, group, rows, nil)
}

func (store *Store) replaceStatcastSnapshot(season int64, group string, rows []StatcastWrite, afterWrite func(int) error) (int, error) {
	const operation = "replace Statcast snapshot"
	if season <= 0 || group != "batting" && group != "pitching" {
		return 0, fmt.Errorf("%s: season and stat group must be valid; correct the value and retry", operation)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("%s: a complete nonempty snapshot is required; correct the value and retry", operation)
	}
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.MLBAMID <= 0 || row.Season != season || row.StatGroup != group {
			return 0, fmt.Errorf("%s: every row must match the positive identity, season, and stat group; correct the value and retry", operation)
		}
		if _, exists := seen[row.MLBAMID]; exists {
			return 0, fmt.Errorf("%s: duplicate MLBAM identity %d; correct the value and retry", operation, row.MLBAMID)
		}
		seen[row.MLBAMID] = struct{}{}
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return 0, err
	}
	written := 0
	err = store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		if _, err := executor.ExecContext(ctx, "DELETE FROM statcast_seasons WHERE season=? AND stat_group=?", season, group); err != nil {
			return operationError("clear Statcast snapshot", store.path, err)
		}
		for _, row := range rows {
			playerID, resolveErr := statcastPlayerID(ctx, executor, row.MLBAMID)
			if resolveErr != nil {
				return operationError("resolve Statcast player identity", store.path, resolveErr)
			}
			if playerID == 0 {
				continue
			}
			_, insertErr := executor.ExecContext(ctx, `INSERT INTO statcast_seasons
(player_id,season,stat_group,pa,bbe,xwoba,exit_velo_avg,barrel_pct,hard_hit_pct,sprint_speed,strikeout_pct,walk_pct,ops,fastball_velo,whiff_pct,chase_pct,gb_pct,fetched_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				playerID, season, group, row.PlateAppearances, row.BattedBallEvents, row.XWOBA, row.ExitVeloAverage, row.BarrelPercent, row.HardHitPercent, row.SprintSpeed, row.StrikeoutPercent, row.WalkPercent, row.OPS, row.FastballVelo, row.WhiffPercent, row.ChasePercent, row.GroundBallPercent, now)
			if insertErr != nil {
				return operationError("write Statcast snapshot", store.path, insertErr)
			}
			written++
			if afterWrite != nil {
				if hookErr := afterWrite(written); hookErr != nil {
					return fmt.Errorf("inject Statcast replacement failure: %w", hookErr)
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

func statcastPlayerID(ctx context.Context, executor sqlExecutor, mlbamID int64) (int64, error) {
	var playerID int64
	err := executor.QueryRowContext(ctx, `SELECT id FROM players WHERE mlbam_id=?
ORDER BY CASE WHEN mlbam_match_source='seed' THEN 0 ELSE 1 END DESC,yahoo_player_id IS NULL,id LIMIT 1`, mlbamID).Scan(&playerID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return playerID, err
}
