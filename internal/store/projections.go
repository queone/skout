package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ProjectionWrite is one resolved FanGraphs projection source and role row.
type ProjectionWrite struct {
	MLBAMID   int64
	Season    int64
	Source    string
	StatGroup string
	PA        float64
	IP        float64
	HR        float64
	R         float64
	RBI       float64
	SB        float64
	AVG       float64
	OBP       float64
	SLG       float64
	ERA       float64
	WHIP      float64
	K         float64
	W         float64
	SV        float64
	BB        float64
}

// ProjectionRow is the durable projection read model.
type ProjectionRow = ProjectionWrite

// FanGraphsBattedBallWrite is one MLB-resolved leaderboard row.
type FanGraphsBattedBallWrite struct {
	MLBAMID    int64
	Season     int64
	FlyBallPct float64
	HomeRunFB  float64
}

// CloserWrite is one normalized team and player designation.
type CloserWrite struct {
	Team string
	Name string
}

// ECRWrite is one FantasyPros rank with primary and fallback identities.
type ECRWrite struct {
	YahooPlayerID *int64
	Name          string
	Team          string
	Rank          int64
}

var projectionWeights = map[string]float64{"steamer": 0.40, "zips": 0.35, "atc": 0.25}

// BlendProjections returns the raw rows plus one normalized weighted blend per player and role.
func BlendProjections(rows []ProjectionWrite) ([]ProjectionWrite, error) {
	type key struct {
		mlbamID int64
		season  int64
		group   string
	}
	grouped := make(map[key][]ProjectionWrite)
	seen := make(map[string]struct{}, len(rows))
	output := append([]ProjectionWrite(nil), rows...)
	for _, row := range rows {
		weight, recognized := projectionWeights[row.Source]
		if row.MLBAMID <= 0 || row.Season <= 0 || row.StatGroup != "batting" && row.StatGroup != "pitching" || !recognized || weight <= 0 {
			return nil, fmt.Errorf("blend projections: rows require a positive identity and season, a supported source, and a batting or pitching group; correct the value and retry")
		}
		identity := fmt.Sprintf("%d/%d/%s/%s", row.MLBAMID, row.Season, row.StatGroup, row.Source)
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("blend projections: duplicate source row %s; correct the value and retry", identity)
		}
		seen[identity] = struct{}{}
		grouped[key{row.MLBAMID, row.Season, row.StatGroup}] = append(grouped[key{row.MLBAMID, row.Season, row.StatGroup}], row)
	}
	keys := make([]key, 0, len(grouped))
	for identity := range grouped {
		keys = append(keys, identity)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].mlbamID != keys[j].mlbamID {
			return keys[i].mlbamID < keys[j].mlbamID
		}
		if keys[i].season != keys[j].season {
			return keys[i].season < keys[j].season
		}
		return keys[i].group < keys[j].group
	})
	for _, identity := range keys {
		group := grouped[identity]
		total := 0.0
		for _, row := range group {
			total += projectionWeights[row.Source]
		}
		blend := ProjectionWrite{MLBAMID: identity.mlbamID, Season: identity.season, Source: "blend", StatGroup: identity.group}
		for _, row := range group {
			weight := projectionWeights[row.Source] / total
			blend.PA += row.PA * weight
			blend.IP += row.IP * weight
			blend.HR += row.HR * weight
			blend.R += row.R * weight
			blend.RBI += row.RBI * weight
			blend.SB += row.SB * weight
			blend.AVG += row.AVG * weight
			blend.OBP += row.OBP * weight
			blend.SLG += row.SLG * weight
			blend.ERA += row.ERA * weight
			blend.WHIP += row.WHIP * weight
			blend.K += row.K * weight
			blend.W += row.W * weight
			blend.SV += row.SV * weight
			blend.BB += row.BB * weight
		}
		output = append(output, blend)
	}
	sort.SliceStable(output, func(i, j int) bool {
		left, right := output[i], output[j]
		if left.MLBAMID != right.MLBAMID {
			return left.MLBAMID < right.MLBAMID
		}
		if left.Season != right.Season {
			return left.Season < right.Season
		}
		if left.StatGroup != right.StatGroup {
			return left.StatGroup < right.StatGroup
		}
		return projectionSourceOrder(left.Source) < projectionSourceOrder(right.Source)
	})
	return output, nil
}

// ReplaceFanGraphsSnapshot atomically replaces all FanGraphs-owned season data.
func (store *Store) ReplaceFanGraphsSnapshot(season int64, projections []ProjectionWrite, battedBall []FanGraphsBattedBallWrite, closers []CloserWrite) (int, error) {
	return store.replaceFanGraphsSnapshot(season, projections, battedBall, closers, nil)
}

func (store *Store) replaceFanGraphsSnapshot(season int64, projections []ProjectionWrite, battedBall []FanGraphsBattedBallWrite, closers []CloserWrite, afterStage func(string) error) (int, error) {
	const operation = "replace FanGraphs snapshot"
	if season <= 0 || len(projections) == 0 || len(battedBall) == 0 {
		return 0, fmt.Errorf("%s: complete season datasets are required; correct the value and retry", operation)
	}
	if err := validateProjectionRows(season, projections); err != nil {
		return 0, err
	}
	seenBatted := make(map[int64]struct{}, len(battedBall))
	for _, row := range battedBall {
		if row.MLBAMID <= 0 || row.Season != season {
			return 0, fmt.Errorf("%s: batted-ball rows must match the positive identity and season; correct the value and retry", operation)
		}
		if _, exists := seenBatted[row.MLBAMID]; exists {
			return 0, fmt.Errorf("%s: duplicate batted-ball identity %d; correct the value and retry", operation, row.MLBAMID)
		}
		seenBatted[row.MLBAMID] = struct{}{}
	}
	for _, row := range closers {
		if strings.TrimSpace(row.Team) == "" || strings.TrimSpace(row.Name) == "" {
			return 0, fmt.Errorf("%s: closer rows require team and name; correct the value and retry", operation)
		}
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return 0, err
	}
	written := 0
	err = store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		resolvedProjections, resolveErr := resolvableMLBAMCount(ctx, executor, projectionMLBAMIDs(projections))
		if resolveErr != nil {
			return operationError(operation, store.path, resolveErr)
		}
		resolvedBatted, resolveErr := resolvableMLBAMCount(ctx, executor, battedMLBAMIDs(battedBall))
		if resolveErr != nil {
			return operationError(operation, store.path, resolveErr)
		}
		if resolvedProjections == 0 || resolvedBatted == 0 {
			return fmt.Errorf("%s: snapshot has no resolvable canonical identities; correct the value and retry", operation)
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM player_projections WHERE season=?", season); err != nil {
			return operationError(operation, store.path, err)
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM fangraphs_batted_ball WHERE season=?", season); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, row := range projections {
			playerID, resolveErr := projectionPlayerID(ctx, executor, row.MLBAMID)
			if resolveErr != nil {
				return operationError(operation, store.path, resolveErr)
			}
			if playerID == 0 {
				continue
			}
			_, insertErr := executor.ExecContext(ctx, `INSERT INTO player_projections
(player_id,season,source,stat_group,pa,ip,hr,r,rbi,sb,avg,obp,slg,era,whip,k,w,sv,bb,fetched_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, playerID, season, row.Source, row.StatGroup, row.PA, row.IP, row.HR, row.R, row.RBI, row.SB, row.AVG, row.OBP, row.SLG, row.ERA, row.WHIP, row.K, row.W, row.SV, row.BB, now)
			if insertErr != nil {
				return operationError(operation, store.path, insertErr)
			}
			written++
		}
		if afterStage != nil {
			if hookErr := afterStage("projections"); hookErr != nil {
				return fmt.Errorf("inject FanGraphs replacement failure: %w", hookErr)
			}
		}
		for _, row := range battedBall {
			playerID, resolveErr := projectionPlayerID(ctx, executor, row.MLBAMID)
			if resolveErr != nil {
				return operationError(operation, store.path, resolveErr)
			}
			if playerID == 0 {
				continue
			}
			if _, insertErr := executor.ExecContext(ctx, "INSERT INTO fangraphs_batted_ball(player_id,season,fb_pct,hr_fb_pct,fetched_at) VALUES(?,?,?,?,?)", playerID, season, row.FlyBallPct, row.HomeRunFB, now); insertErr != nil {
				return operationError(operation, store.path, insertErr)
			}
		}
		if afterStage != nil {
			if hookErr := afterStage("batted-ball"); hookErr != nil {
				return fmt.Errorf("inject FanGraphs replacement failure: %w", hookErr)
			}
		}
		if _, err := executor.ExecContext(ctx, "UPDATE players SET is_closer=0 WHERE eligible_positions LIKE '%RP%'"); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, row := range closers {
			if _, err := executor.ExecContext(ctx, `UPDATE players SET is_closer=1 WHERE id=(
SELECT id FROM players WHERE mlb_team=? AND LOWER(name)=LOWER(?) AND eligible_positions LIKE '%RP%'
GROUP BY LOWER(name),mlb_team HAVING COUNT(*)=1)`, strings.ToUpper(strings.TrimSpace(row.Team)), strings.TrimSpace(row.Name)); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		if _, err := executor.ExecContext(ctx, `UPDATE players SET is_closer=1 WHERE id IN (
SELECT p.id FROM players p JOIN mlbam_season_stats s ON s.player_id=p.id AND s.stat_group='pitching' AND s.season=?
WHERE p.eligible_positions LIKE '%RP%' AND NOT EXISTS(
SELECT 1 FROM players c WHERE c.mlb_team=p.mlb_team AND c.is_closer=1)
AND s.sv=(SELECT MAX(s2.sv) FROM mlbam_season_stats s2 JOIN players p2 ON p2.id=s2.player_id
WHERE p2.mlb_team=p.mlb_team AND s2.stat_group='pitching' AND s2.season=?))`, season, season); err != nil {
			return operationError(operation, store.path, err)
		}
		if afterStage != nil {
			if hookErr := afterStage("closers"); hookErr != nil {
				return fmt.Errorf("inject FanGraphs replacement failure: %w", hookErr)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// ReplaceECR atomically replaces FantasyPros ranks for the current player population.
func (store *Store) ReplaceECR(rows []ECRWrite) (int, error) {
	return store.replaceECR(rows, nil)
}

func (store *Store) replaceECR(rows []ECRWrite, afterClear func() error) (int, error) {
	const operation = "replace FantasyPros ECR"
	if len(rows) == 0 {
		return 0, fmt.Errorf("%s: a complete ECR snapshot is required; correct the value and retry", operation)
	}
	for _, row := range rows {
		if strings.TrimSpace(row.Name) == "" || row.Rank <= 0 {
			return 0, fmt.Errorf("%s: every row requires a name and positive rank; correct the value and retry", operation)
		}
		if row.YahooPlayerID != nil && *row.YahooPlayerID <= 0 {
			return 0, fmt.Errorf("%s: Yahoo player identities must be positive; correct the value and retry", operation)
		}
	}
	written := 0
	err := store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		resolvable := 0
		for _, row := range rows {
			count, countErr := ecrIdentityCount(ctx, executor, row)
			if countErr != nil {
				return operationError(operation, store.path, countErr)
			}
			if count == 1 {
				resolvable++
			}
		}
		if resolvable == 0 {
			return fmt.Errorf("%s: snapshot has no unambiguous player identities; correct the value and retry", operation)
		}
		if _, err := executor.ExecContext(ctx, "UPDATE players SET ecr=NULL"); err != nil {
			return operationError(operation, store.path, err)
		}
		if afterClear != nil {
			if hookErr := afterClear(); hookErr != nil {
				return fmt.Errorf("inject ECR replacement failure: %w", hookErr)
			}
		}
		for _, row := range rows {
			var result sql.Result
			var updateErr error
			if row.YahooPlayerID != nil {
				result, updateErr = executor.ExecContext(ctx, "UPDATE players SET ecr=? WHERE yahoo_player_id=?", row.Rank, *row.YahooPlayerID)
			} else {
				result, updateErr = executor.ExecContext(ctx, `UPDATE players SET ecr=? WHERE id=(
SELECT id FROM players WHERE LOWER(name)=LOWER(?) AND mlb_team=?
GROUP BY LOWER(name),mlb_team HAVING COUNT(*)=1)`, row.Rank, strings.TrimSpace(row.Name), strings.ToUpper(strings.TrimSpace(row.Team)))
			}
			if updateErr != nil {
				return operationError(operation, store.path, updateErr)
			}
			changed, changedErr := result.RowsAffected()
			if changedErr != nil {
				return operationError(operation, store.path, changedErr)
			}
			written += int(changed)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// BlendedProjection reads one stored blended projection.
func (store *Store) BlendedProjection(mlbamID, season int64, group string) (*ProjectionRow, error) {
	const operation = "read blended projection"
	if mlbamID <= 0 || season <= 0 || group != "batting" && group != "pitching" {
		return nil, fmt.Errorf("%s: positive identity and season plus a valid stat group are required; correct the value and retry", operation)
	}
	row := store.conn.QueryRowContext(context.Background(), `SELECT ?,season,source,stat_group,pa,ip,hr,r,rbi,sb,avg,obp,slg,era,whip,k,w,sv,bb
FROM player_projections WHERE player_id=(SELECT id FROM players WHERE mlbam_id=?
ORDER BY CASE WHEN mlbam_match_source='seed' THEN 0 ELSE 1 END DESC,id LIMIT 1)
AND season=? AND source='blend' AND stat_group=?`, mlbamID, mlbamID, season, group)
	var output ProjectionRow
	err := row.Scan(&output.MLBAMID, &output.Season, &output.Source, &output.StatGroup, &output.PA, &output.IP, &output.HR, &output.R, &output.RBI, &output.SB, &output.AVG, &output.OBP, &output.SLG, &output.ERA, &output.WHIP, &output.K, &output.W, &output.SV, &output.BB)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	return &output, nil
}

func validateProjectionRows(season int64, rows []ProjectionWrite) error {
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.MLBAMID <= 0 || row.Season != season || row.StatGroup != "batting" && row.StatGroup != "pitching" || row.Source != "steamer" && row.Source != "zips" && row.Source != "atc" && row.Source != "blend" {
			return fmt.Errorf("replace FanGraphs snapshot: projection rows must match the positive identity, season, source, and stat group; correct the value and retry")
		}
		key := fmt.Sprintf("%d/%s/%s", row.MLBAMID, row.Source, row.StatGroup)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("replace FanGraphs snapshot: duplicate projection identity %s; correct the value and retry", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func projectionMLBAMIDs(rows []ProjectionWrite) []int64 {
	output := make([]int64, 0, len(rows))
	for _, row := range rows {
		output = append(output, row.MLBAMID)
	}
	return output
}

func battedMLBAMIDs(rows []FanGraphsBattedBallWrite) []int64 {
	output := make([]int64, 0, len(rows))
	for _, row := range rows {
		output = append(output, row.MLBAMID)
	}
	return output
}

func resolvableMLBAMCount(ctx context.Context, executor sqlExecutor, ids []int64) (int, error) {
	seen := make(map[int64]struct{}, len(ids))
	resolved := 0
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		var exists bool
		if err := executor.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM players WHERE mlbam_id=?)", id).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			resolved++
		}
	}
	return resolved, nil
}

func projectionPlayerID(ctx context.Context, executor sqlExecutor, mlbamID int64) (int64, error) {
	var playerID int64
	err := executor.QueryRowContext(ctx, `SELECT id FROM players WHERE mlbam_id=?
ORDER BY CASE WHEN mlbam_match_source='seed' THEN 0 ELSE 1 END DESC,id LIMIT 1`, mlbamID).Scan(&playerID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return playerID, err
}

func ecrIdentityCount(ctx context.Context, executor sqlExecutor, row ECRWrite) (int64, error) {
	var count int64
	if row.YahooPlayerID != nil {
		err := executor.QueryRowContext(ctx, "SELECT COUNT(*) FROM players WHERE yahoo_player_id=?", *row.YahooPlayerID).Scan(&count)
		return count, err
	}
	err := executor.QueryRowContext(ctx, "SELECT COUNT(*) FROM players WHERE LOWER(name)=LOWER(?) AND mlb_team=?", strings.TrimSpace(row.Name), strings.ToUpper(strings.TrimSpace(row.Team))).Scan(&count)
	return count, err
}

func projectionSourceOrder(source string) int {
	switch source {
	case "steamer":
		return 0
	case "zips":
		return 1
	case "atc":
		return 2
	case "blend":
		return 3
	default:
		return 4
	}
}
