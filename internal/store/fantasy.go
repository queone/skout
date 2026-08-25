package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/queone/skout/internal/domain"
)

// CategoryWrite is one scoring-category persistence row.
type CategoryWrite struct {
	StatID       int64
	Abbreviation string
	Name         string
	SortOrder    int
	DisplayOnly  bool
	Sequence     int64
}

// PositionWrite is one roster-position persistence row.
type PositionWrite struct {
	Position string
	Count    int64
}

// FantasySnapshotWrite is one complete stable Yahoo league snapshot.
type FantasySnapshotWrite struct {
	League      domain.League
	CurrentWeek *int
	Categories  []CategoryWrite
	Positions   []PositionWrite
	Teams       []domain.FantasyTeam
	Players     []domain.FantasyPlayer
	Slots       []domain.FantasyRosterSlot
}

// FantasyCommandSnapshotWrite is one command payload committed with a Yahoo league snapshot.
type FantasyCommandSnapshotWrite struct {
	Dataset string
	Source  string
	Scope   string
	Version string
	Payload string
}

// StoredFantasyTeam is one durable fantasy-team read model.
type StoredFantasyTeam struct {
	TeamKey        string
	Name           string
	ManagerName    string
	TeamID         int64
	WaiverPriority int64
	FAABBalance    int64
	Wins           int64
	Losses         int64
	Ties           int64
	Moves          int64
	Rank           int64
}

// StoredFantasyCategory is one displayed scoring category.
type StoredFantasyCategory struct {
	StatID       int64
	Abbreviation string
	Sequence     int64
}

// StoredFantasyPlayer contains durable Yahoo identity and ownership fields.
type StoredFantasyPlayer struct {
	YahooPlayerID     int64
	MLBAMID           *int64
	Name              string
	MLBTeam           string
	PositionType      string
	EligiblePositions string
	InjuryStatus      string
	YahooRank         *int64
	PercentOwned      *float64
	PercentageStarted *float64
	Owner             *string
	Slot              string
}

// IdentityCandidate is one MLB identity eligible for exact reconciliation.
type IdentityCandidate struct {
	MLBAMID int64
	Name    string
	Team    string
	Role    string
}

// ReplaceFantasySnapshot replaces one complete league snapshot atomically.
func (store *Store) ReplaceFantasySnapshot(snapshot FantasySnapshotWrite) error {
	return store.ReplaceFantasySyncSnapshot(snapshot, nil)
}

// ReplaceFantasySyncSnapshot commits fantasy tables and weekly command payloads together.
func (store *Store) ReplaceFantasySyncSnapshot(snapshot FantasySnapshotWrite, commands []FantasyCommandSnapshotWrite) error {
	const operation = "replace fantasy snapshot"
	if err := validateFantasySnapshot(snapshot); err != nil {
		return err
	}
	for _, command := range commands {
		for field, value := range map[string]string{"dataset": command.Dataset, "source": command.Source, "scope": command.Scope, "snapshot version": command.Version, "payload": command.Payload} {
			if err := validateIdentity(operation, field, value); err != nil {
				return err
			}
		}
		if !json.Valid([]byte(command.Payload)) {
			return fmt.Errorf("%s: command payload is not valid JSON; correct the value and retry", operation)
		}
	}
	now, err := store.capturedUnix(operation)
	if err != nil {
		return err
	}
	leagueKey := snapshot.League.LeagueKey
	return store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		if _, err := executor.ExecContext(ctx, `INSERT INTO yahoo_leagues
(league_key,name,season,num_teams,scoring_type,current_week,synced_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(league_key) DO UPDATE SET name=excluded.name,season=excluded.season,
num_teams=excluded.num_teams,scoring_type=excluded.scoring_type,
current_week=excluded.current_week,synced_at=excluded.synced_at`,
			leagueKey, snapshot.League.Name, snapshot.League.Season, snapshot.League.NumTeams,
			snapshot.League.ScoringType.String(), snapshot.CurrentWeek, now); err != nil {
			return operationError(operation, store.path, err)
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM yahoo_stat_categories WHERE league_key=?", leagueKey); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, row := range snapshot.Categories {
			if _, err := executor.ExecContext(ctx, "INSERT INTO yahoo_stat_categories(league_key,stat_id,abbr,name,sort_order,display_only,seq) VALUES(?,?,?,?,?,?,?)", leagueKey, row.StatID, row.Abbreviation, row.Name, row.SortOrder, boolInteger(row.DisplayOnly), row.Sequence); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM yahoo_roster_positions WHERE league_key=?", leagueKey); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, row := range snapshot.Positions {
			if _, err := executor.ExecContext(ctx, "INSERT INTO yahoo_roster_positions(league_key,position,count) VALUES(?,?,?)", leagueKey, row.Position, row.Count); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		for _, team := range snapshot.Teams {
			if _, err := executor.ExecContext(ctx, `INSERT INTO yahoo_teams
(team_key,league_key,team_id,name,manager_nickname,waiver_priority,faab_balance,wins,losses,ties,moves,rank,synced_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(team_key) DO UPDATE SET league_key=excluded.league_key,team_id=excluded.team_id,
name=excluded.name,manager_nickname=excluded.manager_nickname,
waiver_priority=excluded.waiver_priority,faab_balance=excluded.faab_balance,
wins=excluded.wins,losses=excluded.losses,ties=excluded.ties,moves=excluded.moves,
rank=excluded.rank,synced_at=excluded.synced_at`, team.TeamKey, team.LeagueKey, team.TeamID,
				team.Name, nullable(team.ManagerName), team.WaiverPriority, team.FAABBalance,
				team.Wins, team.Losses, team.Ties, team.Moves, team.Rank, now); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		for _, player := range snapshot.Players {
			positions := make([]string, 0, len(player.EligiblePositions))
			for _, position := range player.EligiblePositions {
				positions = append(positions, position.String())
			}
			if _, err := executor.ExecContext(ctx, `INSERT INTO players
(yahoo_player_id,name,mlb_team,display_position,position_type,eligible_positions,status,percent_owned,pct_started,yahoo_rank,synced_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(yahoo_player_id) DO UPDATE SET name=excluded.name,mlb_team=excluded.mlb_team,
display_position=excluded.display_position,position_type=excluded.position_type,
eligible_positions=excluded.eligible_positions,status=excluded.status,
percent_owned=COALESCE(excluded.percent_owned,players.percent_owned),
pct_started=COALESCE(excluded.pct_started,players.pct_started),
yahoo_rank=COALESCE(excluded.yahoo_rank,players.yahoo_rank),synced_at=excluded.synced_at`,
				player.YahooPlayerID, player.Name, nullable(player.MLBTeam), nullable(player.DisplayPosition),
				nullable(player.PositionType), nullable(strings.Join(positions, ",")), nullable(player.InjuryStatus),
				player.PercentOwned, player.PercentageStarted, player.YahooRank, now); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM yahoo_roster_slots WHERE team_key IN (SELECT team_key FROM yahoo_teams WHERE league_key=?)", leagueKey); err != nil {
			return operationError(operation, store.path, err)
		}
		rows, err := executor.QueryContext(ctx, "SELECT team_key FROM yahoo_teams WHERE league_key=? ORDER BY team_key", leagueKey)
		if err != nil {
			return operationError(operation, store.path, err)
		}
		var storedTeamKeys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return operationError(operation, store.path, err)
			}
			storedTeamKeys = append(storedTeamKeys, key)
		}
		if err := rows.Close(); err != nil {
			return operationError(operation, store.path, err)
		}
		currentTeams := make(map[string]struct{}, len(snapshot.Teams))
		for _, team := range snapshot.Teams {
			currentTeams[team.TeamKey] = struct{}{}
		}
		for _, key := range storedTeamKeys {
			if _, exists := currentTeams[key]; exists {
				continue
			}
			if _, err := executor.ExecContext(ctx, "DELETE FROM yahoo_teams WHERE team_key=?", key); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		rostered := make(map[int64]struct{}, len(snapshot.Slots))
		for _, slot := range snapshot.Slots {
			var playerID int64
			if err := executor.QueryRowContext(ctx, "SELECT id FROM players WHERE yahoo_player_id=?", slot.YahooPlayerID).Scan(&playerID); err != nil {
				return operationError(operation, store.path, err)
			}
			if _, err := executor.ExecContext(ctx, "INSERT INTO yahoo_roster_slots(team_key,player_id,slot_position,synced_at) VALUES(?,?,?,?)", slot.TeamKey, playerID, slot.SlotPosition.String(), now); err != nil {
				return operationError(operation, store.path, err)
			}
			rostered[slot.YahooPlayerID] = struct{}{}
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM yahoo_free_agents WHERE league_key=?", leagueKey); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, player := range snapshot.Players {
			if _, exists := rostered[player.YahooPlayerID]; exists {
				continue
			}
			var playerID int64
			if err := executor.QueryRowContext(ctx, "SELECT id FROM players WHERE yahoo_player_id=?", player.YahooPlayerID).Scan(&playerID); err != nil {
				return operationError(operation, store.path, err)
			}
			if _, err := executor.ExecContext(ctx, "INSERT INTO yahoo_free_agents(league_key,player_id,synced_at) VALUES(?,?,?)", leagueKey, playerID, now); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		if _, err := executor.ExecContext(ctx, "INSERT INTO sync_log(table_name,synced_at) VALUES('rosters',?) ON CONFLICT(table_name) DO UPDATE SET synced_at=excluded.synced_at", now); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, command := range commands {
			if _, err := executor.ExecContext(ctx, `INSERT INTO command_snapshots
(dataset,source,scope,snapshot_version,payload,last_successful_at,stale,error_message)
VALUES(?,?,?,?,?,?,0,'')
ON CONFLICT(dataset,source,scope) DO UPDATE SET
snapshot_version=excluded.snapshot_version,payload=excluded.payload,
last_successful_at=excluded.last_successful_at,stale=0,error_message=''`,
				command.Dataset, command.Source, command.Scope, command.Version, command.Payload, now); err != nil {
				return operationError(operation, store.path, err)
			}
		}
		return nil
	})
}

// FantasyTeams reads one league's teams in standing and provider-key order.
func (store *Store) FantasyTeams(leagueKey string) ([]StoredFantasyTeam, error) {
	if err := validateIdentity("read fantasy teams", "league key", leagueKey); err != nil {
		return nil, err
	}
	rows, err := store.conn.QueryContext(context.Background(), `SELECT team_key,name,COALESCE(manager_nickname,''),team_id,
COALESCE(waiver_priority,0),COALESCE(faab_balance,0),COALESCE(wins,0),COALESCE(losses,0),
COALESCE(ties,0),COALESCE(moves,0),COALESCE(rank,0)
FROM yahoo_teams WHERE league_key=? ORDER BY CASE WHEN COALESCE(rank,0)>0 THEN rank ELSE 999999 END,team_key`, leagueKey)
	if err != nil {
		return nil, operationError("read fantasy teams", store.path, err)
	}
	defer rows.Close()
	var output []StoredFantasyTeam
	for rows.Next() {
		var team StoredFantasyTeam
		if err := rows.Scan(&team.TeamKey, &team.Name, &team.ManagerName, &team.TeamID, &team.WaiverPriority, &team.FAABBalance, &team.Wins, &team.Losses, &team.Ties, &team.Moves, &team.Rank); err != nil {
			return nil, operationError("read fantasy teams", store.path, err)
		}
		team.Name = domain.CleanFantasyTeamName(team.Name)
		output = append(output, team)
	}
	return output, rows.Err()
}

// FantasyCurrentWeek reads one league's current matchup week.
func (store *Store) FantasyCurrentWeek(leagueKey string) (*int, error) {
	var week sql.NullInt64
	err := store.conn.QueryRowContext(context.Background(), "SELECT current_week FROM yahoo_leagues WHERE league_key=?", leagueKey).Scan(&week)
	if err == sql.ErrNoRows || !week.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, operationError("read fantasy current week", store.path, err)
	}
	value := int(week.Int64)
	return &value, nil
}

// FantasySeason reads one league's season.
func (store *Store) FantasySeason(leagueKey string) (*int, error) {
	var season int
	err := store.conn.QueryRowContext(context.Background(), "SELECT season FROM yahoo_leagues WHERE league_key=?", leagueKey).Scan(&season)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, operationError("read fantasy season", store.path, err)
	}
	return &season, nil
}

// FantasyCategories reads displayed categories in league order.
func (store *Store) FantasyCategories(leagueKey string) ([]StoredFantasyCategory, error) {
	rows, err := store.conn.QueryContext(context.Background(), "SELECT stat_id,abbr,seq FROM yahoo_stat_categories WHERE league_key=? AND display_only=0 ORDER BY seq,stat_id", leagueKey)
	if err != nil {
		return nil, operationError("read fantasy categories", store.path, err)
	}
	defer rows.Close()
	var output []StoredFantasyCategory
	for rows.Next() {
		var row StoredFantasyCategory
		if err := rows.Scan(&row.StatID, &row.Abbreviation, &row.Sequence); err != nil {
			return nil, operationError("read fantasy categories", store.path, err)
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// FantasyPositions reads required roster positions in stable order.
func (store *Store) FantasyPositions(leagueKey string) ([]PositionWrite, error) {
	rows, err := store.conn.QueryContext(context.Background(), "SELECT position,count FROM yahoo_roster_positions WHERE league_key=? ORDER BY position", leagueKey)
	if err != nil {
		return nil, operationError("read fantasy positions", store.path, err)
	}
	defer rows.Close()
	var output []PositionWrite
	for rows.Next() {
		var row PositionWrite
		if err := rows.Scan(&row.Position, &row.Count); err != nil {
			return nil, operationError("read fantasy positions", store.path, err)
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// FantasyPlayers reads rostered and free-agent players for one league.
func (store *Store) FantasyPlayers(leagueKey string) ([]StoredFantasyPlayer, error) {
	rows, err := store.conn.QueryContext(context.Background(), `SELECT p.yahoo_player_id,p.mlbam_id,p.name,COALESCE(p.mlb_team,''),
COALESCE(p.position_type,''),COALESCE(p.eligible_positions,''),COALESCE(p.status,''),p.yahoo_rank,
p.percent_owned,p.pct_started,t.name,COALESCE(s.slot_position,'')
FROM players p
LEFT JOIN yahoo_roster_slots s ON s.player_id=p.id AND s.team_key IN (SELECT team_key FROM yahoo_teams WHERE league_key=?)
LEFT JOIN yahoo_teams t ON t.team_key=s.team_key
LEFT JOIN yahoo_free_agents f ON f.player_id=p.id AND f.league_key=?
WHERE s.player_id IS NOT NULL OR f.player_id IS NOT NULL
ORDER BY COALESCE(p.yahoo_rank,999999),p.name,p.yahoo_player_id`, leagueKey, leagueKey)
	if err != nil {
		return nil, operationError("read fantasy players", store.path, err)
	}
	defer rows.Close()
	var output []StoredFantasyPlayer
	for rows.Next() {
		var row StoredFantasyPlayer
		var mlbam sql.NullInt64
		var rank sql.NullInt64
		var owned, started sql.NullFloat64
		var owner sql.NullString
		if err := rows.Scan(&row.YahooPlayerID, &mlbam, &row.Name, &row.MLBTeam, &row.PositionType, &row.EligiblePositions, &row.InjuryStatus, &rank, &owned, &started, &owner, &row.Slot); err != nil {
			return nil, operationError("read fantasy players", store.path, err)
		}
		if mlbam.Valid {
			value := mlbam.Int64
			row.MLBAMID = &value
		}
		if rank.Valid {
			value := rank.Int64
			row.YahooRank = &value
		}
		if owned.Valid {
			value := owned.Float64
			row.PercentOwned = &value
		}
		if started.Valid {
			value := started.Float64
			row.PercentageStarted = &value
		}
		if owner.Valid {
			value := domain.CleanFantasyTeamName(owner.String)
			row.Owner = &value
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// ReconcileMLBIdentities assigns only exact, unique normalized MLB candidates.
func (store *Store) ReconcileMLBIdentities(candidates []IdentityCandidate) (int, error) {
	const operation = "reconcile MLB identities"
	now, err := store.capturedUnix(operation)
	if err != nil {
		return 0, err
	}
	groups := make(map[string]map[int64]struct{})
	for _, candidate := range candidates {
		role := strings.ToUpper(strings.TrimSpace(candidate.Role))
		if candidate.MLBAMID <= 0 || role != "B" && role != "P" {
			continue
		}
		key := fantasyIdentityKey(candidate.Name, candidate.Team, role)
		if groups[key] == nil {
			groups[key] = make(map[int64]struct{})
		}
		groups[key][candidate.MLBAMID] = struct{}{}
	}
	updated := 0
	err = store.immediate(operation, func(ctx context.Context, executor sqlExecutor) error {
		rows, err := executor.QueryContext(ctx, "SELECT id,name,COALESCE(mlb_team,''),COALESCE(position_type,'') FROM players WHERE yahoo_player_id IS NOT NULL AND mlbam_id IS NULL ORDER BY id")
		if err != nil {
			return operationError(operation, store.path, err)
		}
		type unmatched struct {
			id         int64
			name, team string
			role       string
		}
		var players []unmatched
		for rows.Next() {
			var player unmatched
			if err := rows.Scan(&player.id, &player.name, &player.team, &player.role); err != nil {
				rows.Close()
				return operationError(operation, store.path, err)
			}
			players = append(players, player)
		}
		if err := rows.Close(); err != nil {
			return operationError(operation, store.path, err)
		}
		for _, player := range players {
			ids := groups[fantasyIdentityKey(player.name, player.team, player.role)]
			if len(ids) != 1 {
				continue
			}
			var mlbamID int64
			for candidateID := range ids {
				mlbamID = candidateID
			}
			result, err := executor.ExecContext(ctx, "UPDATE players SET mlbam_id=?,mlbam_match_source='name+team+pos',mlbam_matched_at=? WHERE id=? AND mlbam_id IS NULL", mlbamID, now, player.id)
			if err != nil {
				return operationError(operation, store.path, err)
			}
			count, err := result.RowsAffected()
			if err != nil {
				return operationError(operation, store.path, err)
			}
			updated += int(count)
		}
		return nil
	})
	return updated, err
}

func validateFantasySnapshot(snapshot FantasySnapshotWrite) error {
	const operation = "replace fantasy snapshot"
	if err := validateIdentity(operation, "league key", snapshot.League.LeagueKey); err != nil {
		return err
	}
	if strings.TrimSpace(snapshot.League.Name) == "" || snapshot.League.Season <= 0 || snapshot.League.NumTeams <= 0 || len(snapshot.Teams) != snapshot.League.NumTeams || len(snapshot.Categories) == 0 || len(snapshot.Positions) == 0 || len(snapshot.Players) == 0 || len(snapshot.Slots) == 0 {
		return fmt.Errorf("%s: settings, categories, positions, teams, players, and roster slots must be complete; correct the value and retry", operation)
	}
	teamKeys := make(map[string]struct{}, len(snapshot.Teams))
	teamIDs := make(map[int64]struct{}, len(snapshot.Teams))
	for _, team := range snapshot.Teams {
		if strings.TrimSpace(team.TeamKey) == "" || team.LeagueKey != snapshot.League.LeagueKey || team.TeamID <= 0 || strings.TrimSpace(team.Name) == "" {
			return fmt.Errorf("%s: every team requires a unique in-league identity; correct the value and retry", operation)
		}
		if _, exists := teamKeys[team.TeamKey]; exists {
			return fmt.Errorf("%s: every team requires a unique in-league identity; correct the value and retry", operation)
		}
		if _, exists := teamIDs[team.TeamID]; exists {
			return fmt.Errorf("%s: every team requires a unique in-league identity; correct the value and retry", operation)
		}
		teamKeys[team.TeamKey], teamIDs[team.TeamID] = struct{}{}, struct{}{}
	}
	categoryIDs := make(map[int64]struct{}, len(snapshot.Categories))
	for _, category := range snapshot.Categories {
		if category.StatID <= 0 || strings.TrimSpace(category.Abbreviation) == "" || strings.TrimSpace(category.Name) == "" {
			return fmt.Errorf("%s: scoring categories require unique positive identities; correct the value and retry", operation)
		}
		if _, exists := categoryIDs[category.StatID]; exists {
			return fmt.Errorf("%s: scoring categories require unique positive identities; correct the value and retry", operation)
		}
		categoryIDs[category.StatID] = struct{}{}
	}
	positionKeys := make(map[string]struct{}, len(snapshot.Positions))
	for _, position := range snapshot.Positions {
		key := strings.TrimSpace(position.Position)
		if key == "" || position.Count <= 0 {
			return fmt.Errorf("%s: roster positions require unique positive counts; correct the value and retry", operation)
		}
		if _, exists := positionKeys[key]; exists {
			return fmt.Errorf("%s: roster positions require unique positive counts; correct the value and retry", operation)
		}
		positionKeys[key] = struct{}{}
	}
	playerIDs := make(map[int64]struct{}, len(snapshot.Players))
	for _, player := range snapshot.Players {
		role := strings.ToUpper(strings.TrimSpace(player.PositionType))
		if player.YahooPlayerID <= 0 || strings.TrimSpace(player.Name) == "" || role != "B" && role != "P" {
			return fmt.Errorf("%s: players require unique positive identities and B or P roles; correct the value and retry", operation)
		}
		if _, exists := playerIDs[player.YahooPlayerID]; exists {
			return fmt.Errorf("%s: players require unique positive identities and B or P roles; correct the value and retry", operation)
		}
		playerIDs[player.YahooPlayerID] = struct{}{}
	}
	slotKeys := make(map[string]struct{}, len(snapshot.Slots))
	teamSlotCounts := make(map[string]int)
	for _, slot := range snapshot.Slots {
		if _, exists := teamKeys[slot.TeamKey]; !exists {
			return fmt.Errorf("%s: roster ownership is incomplete or mismatched; correct the value and retry", operation)
		}
		if _, exists := playerIDs[slot.YahooPlayerID]; !exists || strings.TrimSpace(slot.SlotPosition.String()) == "" {
			return fmt.Errorf("%s: roster ownership is incomplete or mismatched; correct the value and retry", operation)
		}
		key := fmt.Sprintf("%s/%d", slot.TeamKey, slot.YahooPlayerID)
		if _, exists := slotKeys[key]; exists {
			return fmt.Errorf("%s: roster ownership is incomplete or mismatched; correct the value and retry", operation)
		}
		slotKeys[key], teamSlotCounts[slot.TeamKey] = struct{}{}, teamSlotCounts[slot.TeamKey]+1
	}
	for key := range teamKeys {
		if teamSlotCounts[key] == 0 {
			return fmt.Errorf("%s: roster ownership is incomplete or mismatched; correct the value and retry", operation)
		}
	}
	return nil
}

func fantasyIdentityKey(name, team, role string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(name) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String() + "\x00" + strings.ToUpper(strings.TrimSpace(team)) + "\x00" + strings.ToUpper(strings.TrimSpace(role))
}

func boolInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
