package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/queone/skout/internal/domain"
)

// RosterWrite is one complete roster row awaiting replacement.
type RosterWrite struct {
	MLBAMID      int64
	Name         string
	Position     string
	PrimaryType  string
	Status       string
	JerseyNumber string
}

// SeasonStatWrite contains counting inputs for one player season role.
type SeasonStatWrite struct {
	MLBAMID          int64  `json:"mlbam_id"`
	Name             string `json:"name"`
	TeamAbbreviation string `json:"team_abbreviation"`
	StatGroup        string `json:"stat_group"`
	Games            int64  `json:"games"`
	PlateAppearances int64  `json:"plate_appearances"`
	AtBats           int64  `json:"at_bats"`
	Hits             int64  `json:"hits"`
	HomeRuns         int64  `json:"home_runs"`
	RunsBattedIn     int64  `json:"runs_batted_in"`
	Runs             int64  `json:"runs"`
	StolenBases      int64  `json:"stolen_bases"`
	Walks            int64  `json:"walks"`
	HitByPitch       int64  `json:"hit_by_pitch"`
	TotalBases       int64  `json:"total_bases"`
	Wins             int64  `json:"wins"`
	Saves            int64  `json:"saves"`
	Holds            int64  `json:"holds"`
	Strikeouts       int64  `json:"strikeouts"`
	InningsOuts      int64  `json:"innings_outs"`
	GamesStarted     int64  `json:"games_started"`
	QualityStarts    int64  `json:"quality_starts"`
	HitsAllowed      int64  `json:"hits_allowed"`
	EarnedRuns       int64  `json:"earned_runs"`
	PitcherWalks     int64  `json:"pitcher_walks"`
}

// StoredRosterPlayer combines active roster, identity, ownership, and stats.
type StoredRosterPlayer struct {
	MLBAMID           int64
	Name              string
	Position          string
	PrimaryType       string
	Status            string
	InjuryStatus      string
	IsCloser          bool
	JerseyNumber      string
	EligiblePositions string
	BatSide           string
	PitchHand         string
	YahooRank         *int64
	Owner             *string
	InYahooPool       bool
	PlateAppearances  int64
	OnBasePercentage  float64
	Runs              int64
	HomeRuns          int64
	RunsBattedIn      int64
	StolenBases       int64
	BattingAverage    float64
	InningsPitched    float64
	QualityStarts     int64
	Wins              int64
	Saves             int64
	Strikeouts        int64
	EarnedRunAverage  float64
	WHIP              float64
}

// OwnershipSyncedAt reads the durable Yahoo roster freshness timestamp.
func (store *Store) OwnershipSyncedAt() (*int64, error) {
	var value int64
	err := store.conn.QueryRowContext(context.Background(), "SELECT synced_at FROM sync_log WHERE table_name='rosters'").Scan(&value)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, operationError("read ownership freshness", store.path, err)
	}
	return &value, nil
}

// HitterAverage derives one player's prior-five-completed-season 162-game line.
func (store *Store) HitterAverage(mlbamID, currentSeason int64) (*domain.HitterAverage, error) {
	const operation = "read hitter completed-season average"
	if mlbamID <= 0 || currentSeason <= 0 {
		return nil, fmt.Errorf("%s: positive MLBAM ID and season are required; correct the value and retry", operation)
	}
	var games, pa, runs, homeRuns, rbi, stolenBases, hits, atBats, walks, hbp, totalBases int64
	err := store.conn.QueryRowContext(context.Background(), `SELECT COALESCE(SUM(g),0),COALESCE(SUM(pa),0),COALESCE(SUM(r),0),COALESCE(SUM(hr),0),COALESCE(SUM(rbi),0),COALESCE(SUM(sb),0),COALESCE(SUM(h),0),COALESCE(SUM(ab),0),COALESCE(SUM(bb),0),COALESCE(SUM(hbp),0),COALESCE(SUM(tb),0)
FROM mlbam_season_stats WHERE stat_group='hitting' AND season>=? AND season<? AND player_id=(SELECT id FROM players WHERE mlbam_id=? AND position_type IN ('H','B') ORDER BY CASE WHEN mlbam_match_source='seed' THEN 0 ELSE 1 END,id LIMIT 1)`, currentSeason-5, currentSeason, mlbamID).Scan(&games, &pa, &runs, &homeRuns, &rbi, &stolenBases, &hits, &atBats, &walks, &hbp, &totalBases)
	if err != nil {
		return nil, operationError(operation, store.path, err)
	}
	if games == 0 || atBats == 0 {
		return nil, nil
	}
	scale := func(value int64) int64 { return int64(math.Floor(float64(value)*162/float64(games) + .5)) }
	average := float64(hits) / float64(atBats)
	slugging := float64(totalBases) / float64(atBats)
	denominator := atBats + walks + hbp
	onBase := 0.0
	if denominator > 0 {
		onBase = float64(hits+walks+hbp) / float64(denominator)
	}
	return &domain.HitterAverage{
		PlateAppearances: scale(pa), OnBasePercentage: onBase, OnBasePlusSlugging: onBase + slugging,
		Runs: scale(runs), HomeRuns: scale(homeRuns), RunsBattedIn: scale(rbi),
		StolenBases: scale(stolenBases), BattingAverage: average,
	}, nil
}

// WaiverCandidates reads active-roster opportunity evidence by MLB role.
func (store *Store) WaiverCandidates() ([]domain.WaiverCandidate, error) {
	rows, err := store.conn.QueryContext(context.Background(), `SELECT r.mlbam_id,r.primary_type,COALESCE(MAX(p.eligible_positions),MAX(p.display_position),''),MAX(COALESCE(s.pa,0)),MAX(COALESCE(s.ip,0)),MAX(COALESCE(s.g,0)),MAX(COALESCE(s.gs,0))
FROM mlb_team_active_rosters r
LEFT JOIN players p ON p.mlbam_id=r.mlbam_id AND ((r.primary_type='H' AND p.position_type IN ('H','B')) OR (r.primary_type='P' AND p.position_type='P'))
LEFT JOIN mlbam_season_stats s ON s.player_id=p.id AND s.season=(SELECT MAX(season) FROM mlbam_season_stats) AND s.stat_group=CASE r.primary_type WHEN 'P' THEN 'pitching' ELSE 'hitting' END
WHERE r.status='A' GROUP BY r.mlbam_id,r.primary_type ORDER BY r.primary_type,r.mlbam_id`)
	if err != nil {
		return nil, operationError("read waiver candidates", store.path, err)
	}
	defer rows.Close()
	var output []domain.WaiverCandidate
	for rows.Next() {
		var row domain.WaiverCandidate
		if err := rows.Scan(&row.MLBAMID, &row.Role, &row.Positions, &row.PlateAppearances, &row.InningsPitched, &row.Games, &row.GamesStarted); err != nil {
			return nil, operationError("read waiver candidates", store.path, err)
		}
		output = append(output, row)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("read waiver candidates", store.path, err)
	}
	return output, nil
}

// ReplaceMLBRoster replaces one team's complete validated roster atomically.
func (store *Store) ReplaceMLBRoster(team string, rows []RosterWrite) error {
	const operation = "replace MLB roster"
	if err := validateIdentity(operation, "team abbreviation", team); err != nil {
		return err
	}
	var hadRows bool
	if err := store.conn.QueryRowContext(context.Background(), "SELECT EXISTS(SELECT 1 FROM mlb_team_active_rosters WHERE team_abbr=?)", strings.ToUpper(team)).Scan(&hadRows); err != nil {
		return operationError(operation, store.path, err)
	}
	if len(rows) == 0 && hadRows {
		return fmt.Errorf("%s: empty acquisition cannot replace a prior nonempty roster; correct the value and retry", operation)
	}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key := fmt.Sprintf("%d/%s", row.MLBAMID, row.PrimaryType)
		if row.MLBAMID <= 0 || strings.TrimSpace(row.Name) == "" || row.PrimaryType != "H" && row.PrimaryType != "P" {
			return fmt.Errorf("%s: roster rows require positive unique person-and-role identities; correct the value and retry", operation)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s: roster rows require positive unique person-and-role identities; correct the value and retry", operation)
		}
		seen[key] = struct{}{}
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	team = strings.ToUpper(team)
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		if _, err := executor.ExecContext(ctx, "DELETE FROM mlb_team_active_rosters WHERE team_abbr=?", team); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, row := range rows {
			var playerID int64
			err := executor.QueryRowContext(ctx, "SELECT id FROM players WHERE mlbam_id=? AND position_type=? ORDER BY yahoo_player_id IS NULL,id LIMIT 1", row.MLBAMID, row.PrimaryType).Scan(&playerID)
			if err == nil {
				_, err = executor.ExecContext(ctx, "UPDATE players SET name=?,mlb_team=?,display_position=?,jersey_number=?,synced_at=? WHERE id=?", row.Name, team, row.Position, nullable(row.JerseyNumber), now, playerID)
			} else if err == sql.ErrNoRows {
				_, err = executor.ExecContext(ctx, "INSERT INTO players(mlbam_id,name,mlb_team,display_position,position_type,status,jersey_number,mlbam_match_source,mlbam_matched_at,synced_at) VALUES(?,?,?,?,?,?,?,'40man',?,?)", row.MLBAMID, row.Name, team, row.Position, row.PrimaryType, row.Status, nullable(row.JerseyNumber), now, now)
			}
			if err != nil {
				return operationError(operation, store.path, err)
			}
			if _, err := executor.ExecContext(ctx, "INSERT INTO mlb_team_active_rosters(team_abbr,mlbam_id,primary_type,status,jersey_number,fetched_at) VALUES(?,?,?,?,?,?)", team, row.MLBAMID, row.PrimaryType, row.Status, nullable(row.JerseyNumber), now); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		return nil
	})
}

// ReplaceMLBSeasonStats replaces every supplied season/stat-group scope atomically.
func (store *Store) ReplaceMLBSeasonStats(season int64, rows []SeasonStatWrite) error {
	const operation = "replace MLB season stats"
	if season <= 0 || len(rows) == 0 {
		return fmt.Errorf("%s: season and complete statistic rows are required; correct the value and retry", operation)
	}
	for _, row := range rows {
		if row.MLBAMID <= 0 || row.StatGroup != "hitting" && row.StatGroup != "pitching" {
			return fmt.Errorf("%s: positive MLBAM ID and recognized stat group are required; correct the value and retry", operation)
		}
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		retainedQualityStarts := map[int64]int64{}
		for _, row := range rows {
			if row.StatGroup != "pitching" || row.QualityStarts != 0 {
				continue
			}
			var qualityStarts sql.NullInt64
			if err := executor.QueryRowContext(ctx, "SELECT MAX(s.qs) FROM mlbam_season_stats s JOIN players p ON p.id=s.player_id WHERE p.mlbam_id=? AND s.season=? AND s.stat_group='pitching'", row.MLBAMID, season).Scan(&qualityStarts); err != nil {
				return operationError(operation, store.path, err)
			}
			if qualityStarts.Valid && qualityStarts.Int64 > 0 {
				retainedQualityStarts[row.MLBAMID] = qualityStarts.Int64
			}
		}
		groups := map[string]struct{}{}
		for _, row := range rows {
			groups[row.StatGroup] = struct{}{}
		}
		for group := range groups {
			if _, err := executor.ExecContext(ctx, "DELETE FROM mlbam_season_stats WHERE season=? AND stat_group=?", season, group); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		for _, row := range rows {
			role := "H"
			if row.StatGroup == "pitching" {
				role = "P"
			}
			var playerID int64
			err := executor.QueryRowContext(ctx, "SELECT id FROM players WHERE mlbam_id=? AND position_type=? ORDER BY id LIMIT 1", row.MLBAMID, role).Scan(&playerID)
			if err == sql.ErrNoRows {
				result, insertErr := executor.ExecContext(ctx, "INSERT INTO players(mlbam_id,name,mlb_team,position_type,mlbam_match_source,mlbam_matched_at,synced_at) VALUES(?,?,?,?,'seed',?,?)", row.MLBAMID, row.Name, row.TeamAbbreviation, role, now, now)
				if insertErr != nil {
					return operationError(operation, store.path, insertErr)
				}
				playerID, err = result.LastInsertId()
			}
			if err != nil {
				return operationError(operation, store.path, err)
			}
			actualIP := float64(row.InningsOuts) / 3
			innings := float64(row.InningsOuts/3) + float64(row.InningsOuts%3)/10
			avg := ratio(float64(row.Hits), float64(row.AtBats))
			obp := ratio(float64(row.Hits+row.Walks+row.HitByPitch), float64(row.AtBats+row.Walks+row.HitByPitch))
			slg := ratio(float64(row.TotalBases), float64(row.AtBats))
			era := ratio(9*float64(row.EarnedRuns), actualIP)
			whip := ratio(float64(row.PitcherWalks+row.HitsAllowed), actualIP)
			qualityStarts := row.QualityStarts
			if retained, exists := retainedQualityStarts[row.MLBAMID]; exists {
				qualityStarts = retained
			}
			_, err = executor.ExecContext(ctx, `INSERT INTO mlbam_season_stats
(player_id,season,stat_group,g,pa,ab,h,hr,rbi,r,sb,avg,obp,bb,hbp,tb,slg,ops,w,sv,hld,k,era,whip,ip,gs,qs,h_pit,er,bb_pit,synced_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				playerID, season, row.StatGroup, row.Games, row.PlateAppearances, row.AtBats, row.Hits, row.HomeRuns, row.RunsBattedIn, row.Runs, row.StolenBases, avg, obp, row.Walks, row.HitByPitch, row.TotalBases, slg, obp+slg, row.Wins, row.Saves, row.Holds, row.Strikeouts, era, whip, innings, row.GamesStarted, qualityStarts, row.HitsAllowed, row.EarnedRuns, row.PitcherWalks, now)
			if err != nil {
				return operationError(operation, store.path, err)
			}
		}
		return nil
	})
}

// MLBRoster reads one team in stable role and usage order.
func (store *Store) MLBRoster(team string) ([]StoredRosterPlayer, error) {
	rows, err := store.conn.QueryContext(context.Background(), `SELECT r.mlbam_id,COALESCE(p.name,seed.name,''),COALESCE(p.display_position,seed.display_position,''),r.primary_type,r.status,
COALESCE(p.status,''),COALESCE(p.is_closer,0),COALESCE(r.jersey_number,''),COALESCE(p.eligible_positions,''),COALESCE(p.bat_side,seed.bat_side,''),COALESCE(p.pitch_hand,seed.pitch_hand,''),p.yahoo_rank,
(SELECT t.name FROM yahoo_roster_slots ys JOIN yahoo_teams t ON t.team_key=ys.team_key WHERE ys.player_id=p.id ORDER BY t.team_key LIMIT 1),p.yahoo_player_id IS NOT NULL,
COALESCE(s.pa,0),COALESCE(s.obp,0),COALESCE(s.r,0),COALESCE(s.hr,0),COALESCE(s.rbi,0),COALESCE(s.sb,0),COALESCE(s.avg,0),COALESCE(s.ip,0),COALESCE(s.qs,0),COALESCE(s.w,0),COALESCE(s.sv,0),COALESCE(s.k,0),COALESCE(s.era,0),COALESCE(s.whip,0)
FROM mlb_team_active_rosters r
LEFT JOIN players p ON p.id=(SELECT p2.id FROM players p2 WHERE p2.mlbam_id=r.mlbam_id AND ((r.primary_type='H' AND p2.position_type IN ('H','B')) OR (r.primary_type='P' AND p2.position_type='P')) ORDER BY p2.yahoo_player_id IS NULL,CASE WHEN p2.mlbam_match_source='seed' THEN 0 ELSE 1 END,p2.id LIMIT 1)
LEFT JOIN players seed ON seed.id=(SELECT p3.id FROM players p3 WHERE p3.mlbam_id=r.mlbam_id ORDER BY CASE WHEN p3.mlbam_match_source='seed' THEN 0 ELSE 1 END,p3.id LIMIT 1)
LEFT JOIN mlbam_season_stats s ON s.player_id=COALESCE(seed.id,p.id) AND s.season=(SELECT MAX(season) FROM mlbam_season_stats) AND s.stat_group=CASE r.primary_type WHEN 'P' THEN 'pitching' ELSE 'hitting' END
WHERE r.team_abbr=? ORDER BY CASE r.primary_type WHEN 'H' THEN 0 ELSE 1 END,CASE WHEN r.status='A' THEN 0 ELSE 1 END,CASE r.primary_type WHEN 'H' THEN -COALESCE(s.pa,0) ELSE -COALESCE(s.ip,0) END,COALESCE(p.name,seed.name,''),r.mlbam_id`, strings.ToUpper(team))
	if err != nil {
		return nil, operationError("read MLB roster", store.path, err)
	}
	defer rows.Close()
	var result []StoredRosterPlayer
	for rows.Next() {
		var player StoredRosterPlayer
		var yahooRank sql.NullInt64
		var owner sql.NullString
		if err := rows.Scan(&player.MLBAMID, &player.Name, &player.Position, &player.PrimaryType, &player.Status, &player.InjuryStatus, &player.IsCloser, &player.JerseyNumber, &player.EligiblePositions, &player.BatSide, &player.PitchHand, &yahooRank, &owner, &player.InYahooPool, &player.PlateAppearances, &player.OnBasePercentage, &player.Runs, &player.HomeRuns, &player.RunsBattedIn, &player.StolenBases, &player.BattingAverage, &player.InningsPitched, &player.QualityStarts, &player.Wins, &player.Saves, &player.Strikeouts, &player.EarnedRunAverage, &player.WHIP); err != nil {
			return nil, operationError("read MLB roster", store.path, err)
		}
		if yahooRank.Valid {
			value := yahooRank.Int64
			player.YahooRank = &value
		}
		if owner.Valid {
			value := cleanTeamName(owner.String)
			player.Owner = &value
		}
		result = append(result, player)
	}
	return result, rows.Err()
}

// MLBLocalPlayerCounts reads optional rostered and available counts by club.
func (store *Store) MLBLocalPlayerCounts() (map[string][2]int64, error) {
	rows, err := store.conn.QueryContext(context.Background(), `WITH rostered AS
(SELECT p.mlb_team team,COUNT(DISTINCT s.player_id) count FROM yahoo_roster_slots s JOIN players p ON p.id=s.player_id WHERE p.mlb_team IS NOT NULL GROUP BY p.mlb_team),
total AS (SELECT p.mlb_team team,COUNT(DISTINCT m.player_id) count FROM mlbam_season_stats m JOIN players p ON p.id=m.player_id WHERE p.mlb_team IS NOT NULL GROUP BY p.mlb_team)
SELECT total.team,COALESCE(rostered.count,0),MAX(total.count-COALESCE(rostered.count,0),0) FROM total LEFT JOIN rostered ON rostered.team=total.team WHERE EXISTS(SELECT 1 FROM yahoo_roster_slots) ORDER BY total.team`)
	if err != nil {
		return nil, operationError("read local MLB player counts", store.path, err)
	}
	defer rows.Close()
	result := map[string][2]int64{}
	for rows.Next() {
		var team string
		var rostered, available int64
		if err := rows.Scan(&team, &rostered, &available); err != nil {
			return nil, operationError("read local MLB player counts", store.path, err)
		}
		result[team] = [2]int64{rostered, available}
	}
	return result, rows.Err()
}

// MLBLocalPitcherOwnership reads local ownership keyed by folded full name.
func (store *Store) MLBLocalPitcherOwnership(currentTeamKey string) (map[string][3]bool, error) {
	rows, err := store.conn.QueryContext(context.Background(), `SELECT LOWER(p.name),p.yahoo_player_id IS NOT NULL,
EXISTS(SELECT 1 FROM yahoo_roster_slots ys WHERE ys.player_id=p.id),
EXISTS(SELECT 1 FROM yahoo_roster_slots ys WHERE ys.player_id=p.id AND ys.team_key=?)
FROM players p WHERE p.position_type='P' ORDER BY LOWER(p.name),p.id`, currentTeamKey)
	if err != nil {
		return nil, operationError("read local pitcher ownership", store.path, err)
	}
	defer rows.Close()
	result := map[string][3]bool{}
	for rows.Next() {
		var name string
		var inPool, rostered, mine bool
		if err := rows.Scan(&name, &inPool, &rostered, &mine); err != nil {
			return nil, operationError("read local pitcher ownership", store.path, err)
		}
		result[name] = [3]bool{inPool, rostered, mine}
	}
	return result, rows.Err()
}

func ratio(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func cleanTeamName(value string) string {
	filtered := strings.Map(func(character rune) rune {
		if character == 0xfe0f || character == 0x200d || character >= 0x2600 && character <= 0x27bf || character >= 0x1f000 && character <= 0x1faff {
			return -1
		}
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	return strings.TrimSpace(filtered)
}
