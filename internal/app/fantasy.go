package app

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/analysis"
	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/display"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
	"github.com/queone/skout/internal/transport"
)

// fantasyFreshnessTTL bounds how old the last successful Yahoo sync may be
// before a fantasy read command runs a blocking sync first.
const fantasyFreshnessTTL = 6 * time.Hour

// PlayerPoolOptions contains the frozen hitter and pitcher selector surface.
type PlayerPoolOptions struct {
	Argument string
	Sort     string
	Position string
	Waiver   bool
}

// FantasyService orchestrates durable read-only fantasy-player views.
type FantasyService struct {
	Store           *store.Store
	League          string
	TeamKey         string
	ArchivedSeason  int
	YahooScoreboard func(string, *int) ([]domain.Matchup, error)
	RosterWeek      func(string, int) (domain.RosterWeekStats, error)
	AutoSync        func() error
	Schedule        func(string) ([]providers.ScheduleGame, error)
	Lineups         func() ([]providers.DailyLineup, error)
	HitterGameLog   func(int64, int64) ([]providers.HittingGameLogEntry, error)
	PitcherGameLog  func(int64, int64) ([]providers.PitchingGameLogEntry, error)
	Boxscore        func(int64) (providers.Boxscore, error)
	Now             func() time.Time
	Mode            terminal.ColorMode
}

// NewProductionFantasyService opens the compatible store and public providers.
func NewProductionFantasyService(_ string, leagueOverride string, season int, mode terminal.ColorMode) (*FantasyService, error) {
	settings, err := config.Read()
	if err != nil {
		return nil, fmt.Errorf("player: read configuration: %w", err)
	}
	league := strings.TrimSpace(leagueOverride)
	if league == "" {
		league = strings.TrimSpace(settings.CurrentLeague)
	}
	if league == "" && season == 0 {
		return nil, fmt.Errorf("player: no league selected; run skout st -l <key>")
	}
	database, err := store.Open()
	if err != nil {
		return nil, fmt.Errorf("player: open database: %w", err)
	}
	if season > 0 {
		key, live, seasonErr := seasonScopedLeague(database, league, season)
		if seasonErr != nil {
			_ = database.Close()
			return nil, fmt.Errorf("player: %w", seasonErr)
		}
		if !live {
			return &FantasyService{Store: database, League: key, TeamKey: settings.CurrentTeamKey, ArchivedSeason: season, Now: time.Now, Mode: mode}, nil
		}
	}
	disk, err := cache.Production()
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("player: initialize cache: %w", err)
	}
	http := transport.Production()
	mlb := providers.NewProductionMLBClient(http)
	yahoo := providers.NewProductionYahooPublicClient(http)
	lineups := providers.NewProductionRotoWireClient(http)
	return &FantasyService{
		Store: database, League: league, TeamKey: settings.CurrentTeamKey,
		YahooScoreboard: yahoo.Scoreboard,
		RosterWeek:      yahoo.RosterWeekStats,
		Schedule: func(date string) ([]providers.ScheduleGame, error) {
			result, err := mlb.FetchScheduleCached(date, disk)
			return result.Games, err
		},
		Lineups:        func() ([]providers.DailyLineup, error) { return lineups.FetchCached(disk) },
		HitterGameLog:  mlb.FetchHitterGameLog,
		PitcherGameLog: mlb.FetchPitcherGameLog,
		Boxscore:       mlb.FetchBoxscore,
		Now:            time.Now, Mode: mode,
	}, nil
}

// seasonScopedLeague resolves a season flag to a stored league key, or reports live handling.
func seasonScopedLeague(database *store.Store, league string, season int) (string, bool, error) {
	if league != "" {
		stored, err := database.FantasySeason(league)
		if err != nil {
			return "", false, err
		}
		if stored != nil && *stored == season {
			return league, true, nil
		}
	}
	keys, err := database.LeaguesForSeason(season)
	if err != nil {
		return "", false, err
	}
	if len(keys) == 1 {
		return keys[0], false, nil
	}
	if len(keys) > 1 {
		return "", false, fmt.Errorf("season %d has multiple stored leagues; select one with -l <key>: %s", season, strings.Join(keys, ", "))
	}
	seasons, err := database.FantasySeasons()
	if err != nil {
		return "", false, err
	}
	if len(seasons) == 0 {
		return "", false, fmt.Errorf("no fantasy seasons are stored; run skout sync and retry")
	}
	labels := make([]string, 0, len(seasons))
	for _, value := range seasons {
		labels = append(labels, strconv.Itoa(value))
	}
	return "", false, fmt.Errorf("season %d is not stored; stored seasons: %s", season, strings.Join(labels, ", "))
}

// Close releases the fantasy service store.
func (service *FantasyService) Close() error {
	if service == nil || service.Store == nil {
		return nil
	}
	return service.Store.Close()
}

// ensureFreshYahoo runs one blocking sync when the last successful Yahoo sync
// is missing or older than the fantasy freshness threshold.
func (service *FantasyService) ensureFreshYahoo(command string) error {
	if service.ArchivedSeason > 0 || service.AutoSync == nil {
		return nil
	}
	needs, err := service.Store.NeedsSyncItem("yahoo_public", "fantasy", service.League, store.ItemRefreshPolicy{TTL: fantasyFreshnessTTL, PipelineVersion: syncPipelineVersion})
	if err != nil || !needs {
		return nil
	}
	syncErr := service.AutoSync()
	if syncErr == nil {
		return nil
	}
	state, stateErr := service.Store.SyncItemState("yahoo_public", "fantasy", service.League)
	if stateErr == nil && state != nil && state.LastSuccessfulAt != nil {
		return nil
	}
	return fmt.Errorf("%s: Yahoo sync failed (%v) and no prior sync data exists; check connectivity and run skout sync", command, syncErr)
}

// Roster renders the selected stored fantasy roster with optional live context.
func (service *FantasyService) Roster(query string) (string, error) {
	if err := service.validate("r"); err != nil {
		return "", err
	}
	if err := service.ensureFreshYahoo("r"); err != nil {
		return "", err
	}
	teams, err := service.Store.FantasyTeams(service.League)
	if err != nil {
		return "", fantasyError("r", "read teams", err)
	}
	team, err := selectFantasyTeam(teams, service.TeamKey, query)
	if err != nil {
		return "", fmt.Errorf("r: %w", service.archivedTeamHint(teams, err))
	}
	all, err := service.fantasyPlayers()
	if err != nil {
		return "", fantasyError("r", "read players", err)
	}
	var players []domain.StoredFantasyPlayer
	for _, player := range all {
		if player.Owner != nil && *player.Owner == team.Name {
			players = append(players, player)
		}
	}
	if len(players) == 0 {
		return "", fmt.Errorf("r: the selected team has no durable roster snapshot; run skout sync and retry")
	}
	service.overlayLiveSlots(team.TeamKey, players)
	sortRosterPlayers(players)
	service.populateGameStatuses(players, all)
	output := display.RenderFantasyPlayers(team.Name, players, service.Mode)
	return service.finishFantasyOutput(output)
}

// Totals renders season totals or one selected Yahoo scoring period.
func (service *FantasyService) Totals(weekly string) (string, error) {
	if err := service.validate("rt"); err != nil {
		return "", err
	}
	if err := service.ensureFreshYahoo("rt"); err != nil {
		return "", err
	}
	if weekly != "" {
		return service.weeklyTotals(weekly)
	}
	teams, err := service.Store.FantasyTeams(service.League)
	if err != nil {
		return "", fantasyError("rt", "read teams", err)
	}
	players, err := service.fantasyPlayers()
	if err != nil {
		return "", fantasyError("rt", "read players", err)
	}
	return service.finishFantasyOutput(display.RenderLeagueTotals(teams, players, service.Mode))
}

// Pool renders one role's player pool or a single detail card.
func (service *FantasyService) Pool(role string, options PlayerPoolOptions) (string, error) {
	var command string
	if role == "B" {
		command = "h"
	} else if role == "P" {
		command = "p"
	} else {
		return "", fmt.Errorf("player: role must be B or P")
	}
	if err := service.validate(command); err != nil {
		return "", err
	}
	if err := service.ensureFreshYahoo(command); err != nil {
		return "", err
	}
	players, err := service.fantasyPlayers()
	if err != nil {
		return "", fantasyError(command, "read players", err)
	}
	if options.Waiver {
		available := false
		for _, player := range players {
			available = available || analysis.YahooPickupAvailable(player)
		}
		if !available {
			return "", service.missingYahooAvailability(command)
		}
	}
	service.populateGameStatuses(players, players)
	rolePlayers := make([]domain.StoredFantasyPlayer, 0, len(players))
	for _, player := range players {
		if player.Role == role {
			rolePlayers = append(rolePlayers, player)
		}
	}
	if options.Argument != "" {
		if _, numeric := strconv.ParseUint(options.Argument, 10, 64); numeric != nil {
			return service.playerDetail(command, rolePlayers, options.Argument)
		}
	}
	var selected []domain.StoredFantasyPlayer
	for _, player := range rolePlayers {
		if options.Position != "" && !hasFantasyPosition(player.Positions, options.Position) || options.Waiver && !analysis.YahooPickupAvailable(player) {
			continue
		}
		selected = append(selected, player)
	}
	if options.Waiver && options.Sort == "" {
		candidates, err := service.Store.WaiverCandidates()
		if err != nil {
			return "", fantasyError(command, "read waiver candidates", err)
		}
		sort.SliceStable(selected, func(i, j int) bool {
			left := analysis.WaiverEligible(selected[i], options.Position, candidates)
			right := analysis.WaiverEligible(selected[j], options.Position, candidates)
			if left != right {
				return left
			}
			return fantasyRankLess(selected[i], selected[j])
		})
	} else {
		sortFantasyPool(selected, options.Sort)
	}
	limit := 20
	if options.Argument != "" {
		if value, err := strconv.Atoi(options.Argument); err == nil && value >= 0 {
			limit = value
		}
	}
	if len(selected) > limit {
		selected = selected[:limit]
	}
	title := "HITTERS"
	if role == "P" {
		title = "PITCHERS"
	}
	return service.finishFantasyOutput(display.RenderFantasyPlayers(title, selected, service.Mode))
}

func (service *FantasyService) playerDetail(command string, players []domain.StoredFantasyPlayer, query string) (string, error) {
	query = strings.ToLower(query)
	var matches []domain.StoredFantasyPlayer
	for _, player := range players {
		if strings.Contains(strings.ToLower(player.Name), query) {
			matches = append(matches, player)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%s: no player matches the query", command)
	}
	if len(matches) > 1 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
		names := make([]string, len(matches))
		for index := range matches {
			names[index] = matches[index].Name
		}
		return "", fmt.Errorf("%s: player query is ambiguous; matches: %s", command, strings.Join(names, ", "))
	}
	player := matches[0]
	season, err := service.Store.FantasySeason(service.League)
	if err != nil || season == nil {
		if err == nil {
			err = fmt.Errorf("league season is unavailable")
		}
		return "", fantasyError(command, "read league season", err)
	}
	logs, stale, err := service.gameLogs(player, int64(*season))
	if err != nil {
		return "", fantasyError(command, "load game log", err)
	}
	var average *domain.HitterAverage
	if player.Role == "B" && player.MLBAMID != nil {
		average, err = service.Store.HitterAverage(*player.MLBAMID, int64(*season))
		if err != nil {
			return "", fantasyError(command, "read completed-season average", err)
		}
	}
	next := ""
	if player.MLBAMID != nil {
		group := "batting"
		if player.Role == "P" {
			group = "pitching"
		}
		projection, projectionErr := service.Store.BlendedProjection(*player.MLBAMID, int64(*season), group)
		if projectionErr != nil {
			return "", fantasyError(command, "read blended projection", projectionErr)
		}
		if projection != nil {
			next = nextProjectionLine(player, *projection, logs)
		}
	}
	today := service.now().Format("2006-01-02")
	return service.finishFantasyOutput(display.RenderPlayerDetail(player, logs, average, next, stale, today, service.Mode))
}

func (service *FantasyService) gameLogs(player domain.StoredFantasyPlayer, season int64) ([]domain.PlayerGameLog, bool, error) {
	if player.MLBAMID == nil {
		return nil, false, fmt.Errorf("MLB identity is unavailable; run skout sync and retry")
	}
	scope := strconv.FormatInt(*player.MLBAMID, 10)
	logs := make([]domain.PlayerGameLog, 0)
	var refreshErr error
	if player.Role == "P" {
		if service.PitcherGameLog == nil {
			refreshErr = fmt.Errorf("pitcher game-log provider is unavailable")
		} else if rows, err := service.PitcherGameLog(*player.MLBAMID, season); err != nil {
			refreshErr = err
		} else {
			for _, row := range rows {
				qs := 0
				if inningsValue(row.Stat.InningsPitched) >= 6 && row.Stat.EarnedRuns <= 3 {
					qs = 1
				}
				logs = append(logs, domain.PlayerGameLog{Date: row.Date, GameID: row.GameID, Opponent: fmt.Sprintf("%s %s", map[bool]string{true: "v", false: "@"}[row.IsHome], MLBTeamAbbreviation(row.OpponentTeamID)), Line: fmt.Sprintf("IP %s  QS %d  W %d  SV %d  K %d  ERA %s  WHIP %s", row.Stat.InningsPitched, qs, row.Stat.Wins, row.Stat.Saves, row.Stat.Strikeouts, row.Stat.ERA, row.Stat.WHIP)})
			}
		}
	} else {
		if service.HitterGameLog == nil {
			refreshErr = fmt.Errorf("hitter game-log provider is unavailable")
		} else if rows, err := service.HitterGameLog(*player.MLBAMID, season); err != nil {
			refreshErr = err
		} else {
			for _, row := range rows {
				prefix := ""
				if !row.IsHome {
					prefix = "@"
				}
				logs = append(logs, domain.PlayerGameLog{Date: row.Date, GameID: row.GameID, Opponent: prefix + MLBTeamAbbreviation(row.OpponentTeamID), Line: fmt.Sprintf("PA %d  AB %d  H %d  R %d  HR %d  RBI %d  SB %d  AVG %s  OBP %s  OPS %s", row.Stat.PlateAppearances, row.Stat.AtBats, row.Stat.Hits, row.Stat.Runs, row.Stat.HomeRuns, row.Stat.RBI, row.Stat.StolenBases, row.Stat.Average, row.Stat.OnBasePercentage, row.Stat.OPS)})
			}
			logs, refreshErr = service.enrichHitterLogs(player, logs)
		}
	}
	if refreshErr == nil {
		if !validPlayerGameLogs(logs, player.Role) {
			return nil, false, fmt.Errorf("game-log response is incomplete")
		}
		payload, err := json.Marshal(logs)
		if err == nil {
			err = service.Store.SaveCommandSnapshot("player-game-log", "mlb", scope, "v1", string(payload))
		}
		return logs, false, err
	}
	_, _ = service.Store.MarkCommandSnapshotStale("player-game-log", "mlb", scope, refreshErr.Error())
	snapshot, err := service.Store.CommandSnapshot("player-game-log", "mlb", scope)
	if err != nil {
		return nil, false, err
	}
	if snapshot == nil || snapshot.SnapshotVersion != "v1" || json.Unmarshal([]byte(snapshot.Payload), &logs) != nil || !validPlayerGameLogs(logs, player.Role) {
		return nil, false, fmt.Errorf("game log unavailable (%v); verify connectivity and retry", refreshErr)
	}
	return logs, true, nil
}

func (service *FantasyService) enrichHitterLogs(player domain.StoredFantasyPlayer, logs []domain.PlayerGameLog) ([]domain.PlayerGameLog, error) {
	byGame := make(map[int64]domain.PlayerGameLog, len(logs))
	byDate := make(map[string][]domain.PlayerGameLog, len(logs))
	for _, row := range logs {
		byGame[row.GameID] = row
		byDate[row.Date] = append(byDate[row.Date], row)
	}
	var output []domain.PlayerGameLog
	for offset := 9; offset >= 0; offset-- {
		date := service.now().AddDate(0, 0, -offset).Format("2006-01-02")
		games, scheduleAvailable := service.optionalSchedule(date)
		if !scheduleAvailable {
			if rows := byDate[date]; len(rows) > 0 {
				output = append(output, rows...)
			} else {
				output = append(output, domain.PlayerGameLog{Date: date})
			}
			continue
		}
		matched := false
		for index := range games {
			game := &games[index]
			if MLBTeamAbbreviation(game.HomeTeamID) != player.Team && MLBTeamAbbreviation(game.AwayTeamID) != player.Team {
				continue
			}
			matched = true
			notStarted := date == service.now().Format("2006-01-02") && gameNotStarted(game.DetailedState)
			isHome := MLBTeamAbbreviation(game.HomeTeamID) == player.Team
			opponent := MLBTeamAbbreviation(game.AwayTeamID)
			if !isHome {
				opponent = "@" + MLBTeamAbbreviation(game.HomeTeamID)
			}
			row := byGame[game.GameID]
			row.Date, row.GameID, row.Opponent, row.Status = date, game.GameID, opponent, gameResult(*game, isHome)
			if !notStarted && player.MLBAMID != nil {
				if boxscore := service.optionalBoxscore(game.GameID); boxscore != nil {
					team := boxscore.Away
					if isHome {
						team = boxscore.Home
					}
					for index, id := range team.BattingOrder {
						if id == *player.MLBAMID {
							row.BattingOrder = index + 1
						}
					}
					if row.Line == "" {
						row.Line = boxscoreHittingLine(team.Players[*player.MLBAMID].Batting)
					}
				}
			}
			output = append(output, row)
		}
		if !matched {
			output = append(output, domain.PlayerGameLog{Date: date})
		}
	}
	return output, nil
}

func (service *FantasyService) optionalSchedule(date string) ([]providers.ScheduleGame, bool) {
	if service.Schedule == nil {
		return nil, false
	}
	rows, err := service.Schedule(date)
	if err == nil {
		if validScheduleSnapshot(rows) {
			if payload, encodeErr := json.Marshal(rows); encodeErr == nil {
				_ = service.Store.SaveCommandSnapshot("player_card_schedule", "mlbam", date, "v1", string(payload))
			}
			return rows, true
		}
		err = fmt.Errorf("schedule response is incomplete")
	}
	_, _ = service.Store.MarkCommandSnapshotStale("player_card_schedule", "mlbam", date, err.Error())
	snapshot, _ := service.Store.CommandSnapshot("player_card_schedule", "mlbam", date)
	if snapshot == nil || snapshot.SnapshotVersion != "v1" || json.Unmarshal([]byte(snapshot.Payload), &rows) != nil || !validScheduleSnapshot(rows) {
		return nil, false
	}
	return rows, true
}

func (service *FantasyService) optionalBoxscore(gameID int64) *providers.Boxscore {
	if service.Boxscore == nil {
		return nil
	}
	scope := strconv.FormatInt(gameID, 10)
	row, err := service.Boxscore(gameID)
	if err == nil {
		if validBoxscoreSnapshot(row) {
			if payload, encodeErr := json.Marshal(row); encodeErr == nil {
				_ = service.Store.SaveCommandSnapshot("player_card_boxscore", "mlbam", scope, "v1", string(payload))
			}
			return &row
		}
		err = fmt.Errorf("boxscore response is incomplete")
	}
	_, _ = service.Store.MarkCommandSnapshotStale("player_card_boxscore", "mlbam", scope, err.Error())
	snapshot, _ := service.Store.CommandSnapshot("player_card_boxscore", "mlbam", scope)
	if snapshot == nil || snapshot.SnapshotVersion != "v1" || json.Unmarshal([]byte(snapshot.Payload), &row) != nil || !validBoxscoreSnapshot(row) {
		return nil
	}
	return &row
}

func validPlayerGameLogs(logs []domain.PlayerGameLog, role string) bool {
	if logs == nil {
		return false
	}
	for _, row := range logs {
		if !domain.IsValidISODate(row.Date) {
			return false
		}
		if role == "P" && (row.GameID <= 0 || strings.TrimSpace(row.Opponent) == "" || strings.TrimSpace(row.Line) == "") {
			return false
		}
		if role != "P" && row.GameID > 0 && strings.TrimSpace(row.Opponent) == "" {
			return false
		}
	}
	return true
}

func validScheduleSnapshot(rows []providers.ScheduleGame) bool {
	if rows == nil {
		return false
	}
	for _, row := range rows {
		if row.GameID <= 0 || row.AwayTeamID <= 0 || row.HomeTeamID <= 0 {
			return false
		}
	}
	return true
}

func validBoxscoreSnapshot(row providers.Boxscore) bool {
	return row.Away.Players != nil && row.Home.Players != nil
}

func (service *FantasyService) weeklyTotals(requested string) (string, error) {
	current, err := service.Store.FantasyCurrentWeek(service.League)
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("league current week is unavailable")
		}
		return "", fantasyError("rt", "read current week", err)
	}
	week := *current
	var matchup domain.Matchup
	stale := false
	if requested == "true" {
		matchup, stale, err = service.weeklyMatchup(week)
	} else if value, parseErr := strconv.Atoi(requested); parseErr == nil {
		if value <= 0 {
			return "", fmt.Errorf("rt: week must be positive")
		}
		matchup, stale, err = service.weeklyMatchup(value)
	} else {
		if !domain.IsValidISODate(requested) {
			return "", fmt.Errorf("rt: weekly date must use YYYY-MM-DD")
		}
		found := false
		for candidate := 1; candidate <= *current; candidate++ {
			var row domain.Matchup
			var rowStale bool
			row, rowStale, err = service.weeklyMatchup(candidate)
			if err != nil {
				break
			}
			if row.WeekStart <= requested && requested <= row.WeekEnd {
				matchup, stale, found = row, rowStale, true
				break
			}
		}
		if err == nil && !found {
			err = fmt.Errorf("date is outside the available Yahoo matchup weeks")
		}
	}
	if err != nil {
		return "", fantasyError("rt", "load weekly matchup", err)
	}
	var selected *domain.MatchupTeam
	for index := range matchup.Teams {
		if matchup.Teams[index].TeamKey == service.TeamKey {
			selected = &matchup.Teams[index]
		}
	}
	if selected == nil {
		return "", fmt.Errorf("rt: selected team has no weekly matchup")
	}
	categories, err := service.Store.FantasyCategories(service.League)
	if err != nil {
		return "", fantasyError("rt", "read categories", err)
	}
	output := display.RenderWeeklyTotals(selected.Name, fmt.Sprintf("WEEK %d", matchup.Week), *selected, categories, stale && service.ArchivedSeason == 0, service.Mode)
	if service.ArchivedSeason > 0 {
		output = display.ArchivedNotice(service.ArchivedSeason, service.Mode) + output
	}
	return output, nil
}

func (service *FantasyService) weeklyMatchup(week int) (domain.Matchup, bool, error) {
	scope := fmt.Sprintf("%s:%d", service.League, week)
	var rows []domain.Matchup
	if service.YahooScoreboard != nil {
		rows, _ = service.YahooScoreboard(service.League, &week)
	}
	if validMatchupRows(rows) {
		payload, err := json.Marshal(rows)
		if err == nil {
			err = service.Store.SaveCommandSnapshot("match_scoreboard", "yahoo", scope, "v1", string(payload))
		}
		if err != nil {
			return domain.Matchup{}, false, err
		}
	} else {
		if service.YahooScoreboard != nil {
			_, _ = service.Store.MarkCommandSnapshotStale("match_scoreboard", "yahoo", scope, "Yahoo scoreboard unavailable")
		}
		snapshot, err := service.Store.CommandSnapshot("match_scoreboard", "yahoo", scope)
		if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" || json.Unmarshal([]byte(snapshot.Payload), &rows) != nil || !validMatchupRows(rows) {
			return domain.Matchup{}, false, fmt.Errorf("requested week has no complete matchup snapshot")
		}
		for _, row := range rows {
			if row.Week == week && matchupHasTeam(row, service.TeamKey) {
				return row, true, nil
			}
		}
	}
	for _, row := range rows {
		if row.Week == week && matchupHasTeam(row, service.TeamKey) {
			return row, false, nil
		}
	}
	return domain.Matchup{}, false, fmt.Errorf("requested week has no matchup")
}

func (service *FantasyService) populateGameStatuses(players, identities []domain.StoredFantasyPlayer) {
	if service.Schedule == nil {
		return
	}
	games, err := service.Schedule(service.now().Format("2006-01-02"))
	if err != nil {
		return
	}
	if service.Lineups != nil {
		if lineups, lineupErr := service.Lineups(); lineupErr == nil {
			overlayLineups(games, lineups, identities)
		}
	}
	applyGameStatuses(players, games)
}

func overlayLineups(games []providers.ScheduleGame, lineups []providers.DailyLineup, players []domain.StoredFantasyPlayer) {
	for _, lineup := range lineups {
		if !lineup.Confirmed {
			continue
		}
		for index := range games {
			if MLBTeamAbbreviation(games[index].AwayTeamID) != lineup.AwayTeam || MLBTeamAbbreviation(games[index].HomeTeamID) != lineup.HomeTeam {
				continue
			}
			if matched := matchedLineup(players, lineup.AwayTeam, lineup.AwayPlayers); matched != nil {
				games[index].AwayLineup = matched
			}
			if matched := matchedLineup(players, lineup.HomeTeam, lineup.HomePlayers); matched != nil {
				games[index].HomeLineup = matched
			}
			if id := uniqueFantasyIdentity(players, lineup.AwayTeam, lineup.AwayPitcher); id != nil {
				games[index].AwayProbablePitcherID = id
			}
			if id := uniqueFantasyIdentity(players, lineup.HomeTeam, lineup.HomePitcher); id != nil {
				games[index].HomeProbablePitcherID = id
			}
		}
	}
}

func matchedLineup(players []domain.StoredFantasyPlayer, team string, names []string) []providers.LineupPlayer {
	var output []providers.LineupPlayer
	matched := 0
	for _, name := range names {
		id := uniqueFantasyIdentity(players, team, name)
		value := int64(0)
		if id != nil {
			value, matched = *id, matched+1
		}
		output = append(output, providers.LineupPlayer{PersonID: value, FullName: name})
	}
	if matched < 7 {
		return nil
	}
	return output
}

func uniqueFantasyIdentity(players []domain.StoredFantasyPlayer, team, name string) *int64 {
	var match *int64
	for _, player := range players {
		if player.MLBAMID == nil || player.Team != team || normalizeFantasyName(player.Name) != normalizeFantasyName(name) {
			continue
		}
		if match != nil && *match != *player.MLBAMID {
			return nil
		}
		value := *player.MLBAMID
		match = &value
	}
	return match
}

func applyGameStatuses(players []domain.StoredFantasyPlayer, games []providers.ScheduleGame) {
	for index := range players {
		player := &players[index]
		for _, game := range games {
			away := MLBTeamAbbreviation(game.AwayTeamID) == player.Team
			home := MLBTeamAbbreviation(game.HomeTeamID) == player.Team
			if !away && !home {
				continue
			}
			opponent := MLBTeamAbbreviation(game.AwayTeamID)
			location := "v " + opponent
			lineup := game.HomeLineup
			probable := game.HomeProbablePitcherID
			if away {
				opponent, location, lineup, probable = MLBTeamAbbreviation(game.HomeTeamID), "@ "+MLBTeamAbbreviation(game.HomeTeamID), game.AwayLineup, game.AwayProbablePitcherID
			}
			_ = opponent
			player.GameIndicator = domain.GameIndicator{}
			if player.MLBAMID != nil {
				if player.Role == "P" && probable != nil && *probable == *player.MLBAMID {
					player.GameIndicator.Kind = domain.GameIndicatorStartingPitcher
				} else if player.Role == "B" && len(lineup) > 0 {
					player.GameIndicator.Kind = domain.GameIndicatorOutOfLineup
					for order, entry := range lineup {
						if entry.PersonID == *player.MLBAMID {
							player.GameIndicator = domain.GameIndicator{Kind: domain.GameIndicatorBattingOrder, Order: order + 1}
						}
					}
				}
			}
			marker := ""
			if player.GameIndicator.Kind == domain.GameIndicatorBattingOrder {
				marker = strconv.Itoa(player.GameIndicator.Order)
			} else if player.GameIndicator.Kind == domain.GameIndicatorStartingPitcher || player.GameIndicator.Kind == domain.GameIndicatorOutOfLineup {
				marker = "●"
			}
			if strings.EqualFold(game.DetailedState, "Final") {
				player.GameStatus = "Final " + location
				if game.Linescore != nil {
					player.GameStatus = fmt.Sprintf("Final %d-%d %s", game.Linescore.AwayRuns, game.Linescore.HomeRuns, location)
				}
			} else if !gameNotStarted(game.DetailedState) && game.Linescore != nil {
				mine, theirs := game.Linescore.HomeRuns, game.Linescore.AwayRuns
				if away {
					mine, theirs = game.Linescore.AwayRuns, game.Linescore.HomeRuns
				}
				player.GameStatus = fmt.Sprintf("%s%s %d-%d %s %s", firstRune(game.Linescore.InningState), game.Linescore.InningOrdinal, mine, theirs, marker, location)
			} else {
				gameTime := game.GameDate
				if parsed, err := time.Parse(time.RFC3339, game.GameDate); err == nil {
					gameTime = parsed.Local().Format("3:04p")
				}
				player.GameStatus = fmt.Sprintf("%s %s %s", gameTime, marker, location)
			}
			break
		}
	}
}

// overlayLiveSlots replaces stored roster slots with the live current-week
// slots so same-day lineup moves render correctly; stored slots stand when the
// live fetch and its snapshot fallback are both unavailable.
func (service *FantasyService) overlayLiveSlots(teamKey string, players []domain.StoredFantasyPlayer) {
	if service.ArchivedSeason > 0 || service.RosterWeek == nil {
		return
	}
	week, err := service.Store.FantasyCurrentWeek(service.League)
	if err != nil || week == nil {
		return
	}
	scope := fmt.Sprintf("%s:%d", teamKey, *week)
	live, err := service.RosterWeek(teamKey, *week)
	if err == nil && validRoster(live, teamKey, *week) {
		if payload, encodeErr := json.Marshal(live); encodeErr == nil {
			_ = service.Store.SaveCommandSnapshot("match_roster", "yahoo", scope, "v2", string(payload))
		}
	} else {
		snapshot, readErr := service.Store.CommandSnapshot("match_roster", "yahoo", scope)
		if readErr != nil || snapshot == nil || snapshot.SnapshotVersion != "v2" || json.Unmarshal([]byte(snapshot.Payload), &live) != nil || !validRoster(live, teamKey, *week) {
			return
		}
	}
	slots := make(map[int64]string, len(live.Players))
	for _, player := range live.Players {
		slots[player.YahooPlayerID] = player.SlotPosition.String()
	}
	for index := range players {
		player := &players[index]
		if player.YahooPlayerID == nil {
			continue
		}
		if slot, ok := slots[*player.YahooPlayerID]; ok && slot != "" && slot != "--" {
			value := slot
			player.Slot = &value
		}
	}
}

// fantasyPlayers reads league players, pinning stats to the archived season when one is selected.
func (service *FantasyService) fantasyPlayers() ([]domain.StoredFantasyPlayer, error) {
	if service.ArchivedSeason > 0 {
		return service.Store.FantasyPlayersForSeason(service.League, service.ArchivedSeason)
	}
	return service.Store.FantasyPlayers(service.League)
}

// finishFantasyOutput labels archived output or applies the live staleness notice.
func (service *FantasyService) finishFantasyOutput(output string) (string, error) {
	if service.ArchivedSeason > 0 {
		return display.ArchivedNotice(service.ArchivedSeason, service.Mode) + output, nil
	}
	return service.yahooNotice(output)
}

// archivedTeamHint replaces a failed current-team match with the archived season's team list.
func (service *FantasyService) archivedTeamHint(teams []store.StoredFantasyTeam, err error) error {
	if service.ArchivedSeason == 0 || len(teams) == 0 {
		return err
	}
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		names = append(names, team.Name)
	}
	return fmt.Errorf("%v (archived season %d teams: %s)", err, service.ArchivedSeason, strings.Join(names, ", "))
}

func (service *FantasyService) yahooNotice(output string) (string, error) {
	run, err := service.Store.LatestSyncRun(store.SyncLive)
	if err != nil {
		return "", err
	}
	if run != nil && run.Status == "failed" {
		return "STALE — showing the last complete Yahoo roster and player-pool snapshot.\n" + output, nil
	}
	return output, nil
}

func (service *FantasyService) missingYahooAvailability(command string) error {
	run, err := service.Store.LatestSyncRun(store.SyncLive)
	if err != nil {
		return fantasyError(command, "read sync state", err)
	}
	message := "Yahoo available-player data is missing"
	if run != nil {
		switch run.Status {
		case "failed":
			message += " because the latest sync failed; run skout st for provider details"
		case "running":
			message += " because a sync is still running; wait for it to finish and retry"
		case "complete":
			message = "the latest Yahoo sync returned no available players"
		}
	}
	return fmt.Errorf("%s: %s", command, message)
}

func (service *FantasyService) validate(command string) error {
	if service == nil || service.Store == nil || strings.TrimSpace(service.League) == "" {
		return fmt.Errorf("%s: runtime boundaries are incomplete; reinstall skout", command)
	}
	return nil
}

func (service *FantasyService) now() time.Time {
	if service.Now == nil {
		return time.Now()
	}
	return service.Now()
}

func selectFantasyTeam(teams []store.StoredFantasyTeam, selected, query string) (store.StoredFantasyTeam, error) {
	var matches []store.StoredFantasyTeam
	if query == "" {
		for _, team := range teams {
			if team.TeamKey == selected {
				matches = append(matches, team)
			}
		}
	} else {
		needle := strings.ToLower(query)
		for _, team := range teams {
			if strings.Contains(strings.ToLower(team.Name), needle) || strings.Contains(strings.ToLower(team.ManagerName), needle) {
				matches = append(matches, team)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return store.StoredFantasyTeam{}, fmt.Errorf("no team matches the query; use `skout st` to list teams")
	}
	names := make([]string, len(matches))
	for index := range matches {
		names[index] = matches[index].Name
	}
	sort.Strings(names)
	return store.StoredFantasyTeam{}, fmt.Errorf("query is ambiguous; matches: %s; use a longer team or manager name", strings.Join(names, ", "))
}

func sortRosterPlayers(players []domain.StoredFantasyPlayer) {
	order := map[string]int{"C": 0, "1B": 1, "2B": 2, "3B": 3, "SS": 4, "OF": 5, "LF": 5, "CF": 5, "RF": 5, "UTIL": 6, "SP": 7, "RP": 8, "P": 9, "BN": 10, "IL": 11, "IL10": 11, "IL15": 11, "IL60": 11}
	sort.SliceStable(players, func(i, j int) bool {
		left, right := 12, 12
		if players[i].Slot != nil {
			if value, ok := order[strings.ToUpper(*players[i].Slot)]; ok {
				left = value
			}
		}
		if players[j].Slot != nil {
			if value, ok := order[strings.ToUpper(*players[j].Slot)]; ok {
				right = value
			}
		}
		if left != right {
			return left < right
		}
		return fantasyRankLess(players[i], players[j])
	})
}

func sortFantasyPool(players []domain.StoredFantasyPlayer, field string) {
	field = strings.ToLower(strings.TrimSpace(field))
	sort.SliceStable(players, func(i, j int) bool {
		left, right := players[i], players[j]
		comparison := 0
		switch field {
		case "name", "player":
			comparison = strings.Compare(left.Name, right.Name)
		case "owner":
			comparison = strings.Compare(pointerString(left.Owner), pointerString(right.Owner))
		case "position", "pos":
			comparison = strings.Compare(left.Positions, right.Positions)
		case "team":
			comparison = strings.Compare(left.Team, right.Team)
		case "ecr":
			comparison = compareRank(left.ExpertConsensusRank, right.ExpertConsensusRank)
		case "xwoba":
			comparison = compareOptionalNumber(left.HittingAdvanced[0], right.HittingAdvanced[0], true)
		case "ev", "exitvelo", "exit-velo":
			comparison = compareOptionalNumber(left.HittingAdvanced[1], right.HittingAdvanced[1], true)
		case "brl", "brl%", "barrel", "barrel%":
			comparison = compareOptionalNumber(left.HittingAdvanced[2], right.HittingAdvanced[2], true)
		case "hh", "hh%", "hardhit", "hard-hit%":
			comparison = compareOptionalNumber(left.HittingAdvanced[3], right.HittingAdvanced[3], true)
		case "spd", "sprint":
			comparison = compareOptionalNumber(left.HittingAdvanced[6], right.HittingAdvanced[6], true)
		case "ops":
			comparison = compareOptionalNumber(left.HittingAdvanced[7], right.HittingAdvanced[7], true)
		case "fbv", "velo", "velocity":
			comparison = compareOptionalNumber(left.PitchingAdvanced[0], right.PitchingAdvanced[0], true)
		case "whiff", "whiff%":
			comparison = compareOptionalNumber(left.PitchingAdvanced[1], right.PitchingAdvanced[1], true)
		case "ch", "ch%", "chase", "chase%":
			comparison = compareOptionalNumber(left.PitchingAdvanced[2], right.PitchingAdvanced[2], true)
		case "gb", "gb%":
			comparison = compareOptionalNumber(left.PitchingAdvanced[3], right.PitchingAdvanced[3], true)
		case "k%":
			if left.Role == "P" {
				comparison = compareOptionalNumber(left.PitchingAdvanced[4], right.PitchingAdvanced[4], true)
			} else {
				comparison = compareOptionalNumber(left.HittingAdvanced[4], right.HittingAdvanced[4], false)
			}
		case "bb%":
			if left.Role == "P" {
				comparison = compareOptionalNumber(left.PitchingAdvanced[5], right.PitchingAdvanced[5], false)
			} else {
				comparison = compareOptionalNumber(left.HittingAdvanced[5], right.HittingAdvanced[5], true)
			}
		case "pa":
			comparison = compareNumber(left.Batting[0], right.Batting[0], true)
		case "obp":
			comparison = compareNumber(left.Batting[1], right.Batting[1], true)
		case "r", "runs":
			comparison = compareNumber(left.Batting[2], right.Batting[2], true)
		case "hr", "homers":
			comparison = compareNumber(left.Batting[3], right.Batting[3], true)
		case "rbi":
			comparison = compareNumber(left.Batting[4], right.Batting[4], true)
		case "sb", "steals":
			comparison = compareNumber(left.Batting[5], right.Batting[5], true)
		case "avg":
			comparison = compareNumber(left.Batting[6], right.Batting[6], true)
		case "ip":
			comparison = compareNumber(left.Pitching[0], right.Pitching[0], true)
		case "qs":
			comparison = compareNumber(left.Pitching[1], right.Pitching[1], true)
		case "w", "wins":
			comparison = compareNumber(left.Pitching[2], right.Pitching[2], true)
		case "sv", "saves":
			comparison = compareNumber(left.Pitching[3], right.Pitching[3], true)
		case "k", "strikeouts":
			comparison = compareNumber(left.Pitching[4], right.Pitching[4], true)
		case "era":
			comparison = compareNumber(left.Pitching[5], right.Pitching[5], false)
		case "whip":
			comparison = compareNumber(left.Pitching[6], right.Pitching[6], false)
		default:
			return fantasyRankLess(left, right)
		}
		if comparison == 0 {
			return fantasyRankLess(left, right)
		}
		return comparison < 0
	})
}

func fantasyRankLess(left, right domain.StoredFantasyPlayer) bool {
	if pointerRank(left.Rank) != pointerRank(right.Rank) {
		return pointerRank(left.Rank) < pointerRank(right.Rank)
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return pointerRank(left.YahooPlayerID) < pointerRank(right.YahooPlayerID)
}

func nextProjectionLine(player domain.StoredFantasyPlayer, projection store.ProjectionRow, logs []domain.PlayerGameLog) string {
	if player.Role == "P" {
		window := 3.0
		if analysis.ClassifyPitcherPositions(player.Positions) == analysis.Starter {
			window = 10
		}
		projected := analysis.PitcherWindow{IP: projection.IP, Wins: projection.W, Saves: projection.SV, Strikeouts: projection.K, ERA: projection.ERA, WHIP: projection.WHIP}
		next := analysis.NextPitcherWindow(&projected, recentPitcherWindow(logs, projected, window), window)
		return fmt.Sprintf("NEXT%02dIP  IP %4.1f  QS %3.0f  W %3.0f  SV %3.0f  K %4.0f  ERA %5.2f  WHIP %5.2f", int(window), next.IP, next.QualityStarts, next.Wins, next.Saves, next.Strikeouts, next.ERA, next.WHIP)
	}
	projected := analysis.HitterWindow{PA: projection.PA, Runs: projection.R, HomeRuns: projection.HR, RBI: projection.RBI, StolenBases: projection.SB, Average: projection.AVG, OBP: projection.OBP, OPS: projection.OBP + projection.SLG}
	next := analysis.NextHitterWindow(&projected, recentHitterWindow(logs, projected), 20)
	return fmt.Sprintf("NEXT20PA  PA %3.0f  R %3.0f  HR %3.0f  RBI %3.0f  SB %3.0f  AVG %.3f  OBP %.3f  OPS %.3f", next.PA, next.Runs, next.HomeRuns, next.RBI, next.StolenBases, next.Average, next.OBP, next.OPS)
}

func recentHitterWindow(logs []domain.PlayerGameLog, projection analysis.HitterWindow) *analysis.HitterWindow {
	window := analysis.HitterWindow{Average: projection.Average, OBP: projection.OBP, OPS: projection.OPS}
	var hits, atBats, obpTotal, obpWeight, opsTotal, opsWeight float64
	for _, log := range slices.Backward(logs) {

		pa, ok := fantasyLogNumber(log.Line, "PA")
		if !ok || pa <= 0 {
			continue
		}
		window.PA += pa
		window.Runs += fantasyLogNumberOrZero(log.Line, "R")
		window.HomeRuns += fantasyLogNumberOrZero(log.Line, "HR")
		window.RBI += fantasyLogNumberOrZero(log.Line, "RBI")
		window.StolenBases += fantasyLogNumberOrZero(log.Line, "SB")
		hits += fantasyLogNumberOrZero(log.Line, "H")
		atBats += fantasyLogNumberOrZero(log.Line, "AB")
		if value, found := fantasyLogNumber(log.Line, "OBP"); found {
			obpTotal, obpWeight = obpTotal+value*pa, obpWeight+pa
		}
		if value, found := fantasyLogNumber(log.Line, "OPS"); found {
			opsTotal, opsWeight = opsTotal+value*pa, opsWeight+pa
		}
		if window.PA >= 20 {
			break
		}
	}
	if window.PA == 0 {
		return nil
	}
	if atBats > 0 {
		window.Average = hits / atBats
	}
	if obpWeight > 0 {
		window.OBP = obpTotal / obpWeight
	}
	if opsWeight > 0 {
		window.OPS = opsTotal / opsWeight
	}
	return &window
}

func recentPitcherWindow(logs []domain.PlayerGameLog, projection analysis.PitcherWindow, targetInnings float64) *analysis.PitcherWindow {
	window := analysis.PitcherWindow{ERA: projection.ERA, WHIP: projection.WHIP}
	var eraTotal, eraWeight, whipTotal, whipWeight float64
	for _, log := range slices.Backward(logs) {

		value, ok := fantasyLogText(log.Line, "IP")
		if !ok {
			continue
		}
		innings := inningsValue(value)
		if innings <= 0 {
			continue
		}
		window.IP += innings
		window.QualityStarts += fantasyLogNumberOrZero(log.Line, "QS")
		window.Wins += fantasyLogNumberOrZero(log.Line, "W")
		window.Saves += fantasyLogNumberOrZero(log.Line, "SV")
		window.Strikeouts += fantasyLogNumberOrZero(log.Line, "K")
		if rate, found := fantasyLogNumber(log.Line, "ERA"); found {
			eraTotal, eraWeight = eraTotal+rate*innings, eraWeight+innings
		}
		if rate, found := fantasyLogNumber(log.Line, "WHIP"); found {
			whipTotal, whipWeight = whipTotal+rate*innings, whipWeight+innings
		}
		if window.IP >= targetInnings {
			break
		}
	}
	if window.IP == 0 {
		return nil
	}
	if eraWeight > 0 {
		window.ERA = eraTotal / eraWeight
	}
	if whipWeight > 0 {
		window.WHIP = whipTotal / whipWeight
	}
	return &window
}

func fantasyLogNumber(line, name string) (float64, bool) {
	value, ok := fantasyLogText(line, name)
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	return number, err == nil
}

func fantasyLogNumberOrZero(line, name string) float64 {
	value, _ := fantasyLogNumber(line, name)
	return value
}

func fantasyLogText(line, name string) (string, bool) {
	fields := strings.Fields(line)
	for index := 0; index+1 < len(fields); index += 2 {
		if fields[index] == name {
			return fields[index+1], true
		}
	}
	return "", false
}

func boxscoreHittingLine(stats *providers.BoxscoreBatting) string {
	if stats == nil {
		return ""
	}
	value := func(input *int64) int64 {
		if input == nil {
			return 0
		}
		return *input
	}
	hits, atBats := value(stats.Hits), value(stats.AtBats)
	average := ".000"
	if atBats > 0 {
		average = strings.TrimPrefix(fmt.Sprintf("%.3f", float64(hits)/float64(atBats)), "0")
	}
	return fmt.Sprintf("AB %d  H %d  R %d  HR %d  RBI %d  SB %d  AVG %s", atBats, hits, value(stats.Runs), value(stats.HomeRuns), value(stats.RBI), value(stats.StolenBases), average)
}

func compareRank(left, right *int64) int {
	return compareNumber(float64(pointerRank(left)), float64(pointerRank(right)), false)
}

func compareOptionalNumber(left, right *float64, descending bool) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	return compareNumber(*left, *right, descending)
}

func compareNumber(left, right float64, descending bool) int {
	if left == right {
		return 0
	}
	if descending {
		if left > right {
			return -1
		}
		return 1
	}
	if left < right {
		return -1
	}
	return 1
}

func inningsValue(value string) float64 {
	whole, outs, ok := strings.Cut(value, ".")
	if !ok {
		innings, err := strconv.ParseFloat(value, 64)
		if err != nil || innings < 0 || math.Trunc(innings) != innings {
			return 0
		}
		return innings
	}
	innings, err := strconv.ParseFloat(whole, 64)
	if err != nil || outs != "0" && outs != "1" && outs != "2" {
		return 0
	}
	extra, _ := strconv.ParseFloat(outs, 64)
	return innings + extra/3
}

func hasFantasyPosition(positions, requested string) bool {
	for value := range strings.SplitSeq(positions, ",") {
		if strings.EqualFold(strings.TrimSpace(value), requested) {
			return true
		}
	}
	return false
}

func matchupHasTeam(matchup domain.Matchup, team string) bool {
	return matchup.Teams[0].TeamKey == team || matchup.Teams[1].TeamKey == team
}

func normalizeFantasyName(value string) string {
	var output strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func pointerRank(value *int64) int64 {
	if value == nil {
		return 1<<63 - 1
	}
	return *value
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func fantasyError(command, operation string, err error) error {
	return fmt.Errorf("%s: %s: %w", command, operation, err)
}

func gameNotStarted(state string) bool {
	switch strings.ToLower(state) {
	case "scheduled", "pre-game", "warmup", "preview":
		return true
	}
	return false
}

func gameResult(game providers.ScheduleGame, home bool) string {
	state := strings.ToLower(game.DetailedState)
	if !strings.HasPrefix(state, "final") && state != "game over" && state != "completed early" || game.Linescore == nil {
		return ""
	}
	mine, theirs := game.Linescore.AwayRuns, game.Linescore.HomeRuns
	if home {
		mine, theirs = game.Linescore.HomeRuns, game.Linescore.AwayRuns
	}
	result := "L"
	if mine > theirs {
		result = "W"
	}
	return fmt.Sprintf("%s, %d-%d", result, mine, theirs)
}

func firstRune(value string) string {
	for _, character := range value {
		return string(character)
	}
	return ""
}

// MLBTeamAbbreviation returns the stable short label for an MLB team identity.
func MLBTeamAbbreviation(teamID int64) string {
	return map[int64]string{108: "LAA", 109: "AZ", 110: "BAL", 111: "BOS", 112: "CHC", 113: "CIN", 114: "CLE", 115: "COL", 116: "DET", 117: "HOU", 118: "KC", 119: "LAD", 120: "WSH", 121: "NYM", 133: "ATH", 134: "PIT", 135: "SD", 136: "SEA", 137: "SF", 138: "STL", 139: "TB", 140: "TEX", 141: "TOR", 142: "MIN", 143: "PHI", 144: "ATL", 145: "CWS", 146: "MIA", 147: "NYY", 158: "MIL"}[teamID]
}
