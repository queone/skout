package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/display"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
	"github.com/queone/skout/internal/transport"
)

const (
	directoryTTL   = 24 * time.Hour
	rosterTTL      = 24 * time.Hour
	totalsTTL      = 15 * time.Minute
	currentOddsTTL = 30 * time.Minute
	futureOddsTTL  = 12 * time.Hour
	slateTTL       = time.Minute
)

// MLBService contains all injectable dependencies for the public MLB commands.
type MLBService struct {
	Store          *store.Store
	Cache          *cache.Disk
	Directory      func(int64) ([]providers.TeamDirectoryEntry, error)
	Standings      func(int64) ([]providers.TeamStanding, error)
	Roster         func(int64) ([]providers.RosterMember, error)
	Schedule       func(string) ([]providers.ScheduleGame, error)
	ScheduleCached func(string, *cache.Disk) (providers.ScheduleCacheResult, error)
	Hitting        func(int64, string) ([]providers.BulkHittingSplit, error)
	Pitching       func(int64, string) ([]providers.BulkPitchingSplit, error)
	CurrentOdds    func(time.Time) (providers.ESPNSlateLines, error)
	FutureOdds     func(string) ([]providers.OddsSharkGameLine, error)
	Now            func() time.Time
	Location       *time.Location
	Input          io.Reader
	Prompt         io.Writer
	InputTerminal  bool
	PromptTerminal bool
	Mode           terminal.ColorMode
	CurrentTeamKey string
	Debug          bool
	DebugOutput    io.Writer
	MeasureNow     func() time.Time
}

// NewProductionMLBService opens compatible local state and public providers.
func NewProductionMLBService(version string, input io.Reader, prompt io.Writer, inputTerminal, promptTerminal bool, mode terminal.ColorMode) (*MLBService, error) {
	http := transport.Production()
	mlb := providers.NewProductionMLBClient(http)
	espn := providers.NewProductionESPNClient(http, version)
	odds := providers.NewProductionOddsSharkClient(http)
	database, err := store.Open()
	if err != nil {
		return nil, fmt.Errorf("mlb: open database: %w", err)
	}
	disk, err := cache.Production()
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("mlb: initialize cache: %w", err)
	}
	return &MLBService{
		Store: database, Cache: disk,
		Directory: mlb.FetchTeamDirectory, Standings: mlb.FetchStandings,
		Roster: mlb.FetchRoster, Schedule: mlb.FetchSchedule,
		ScheduleCached: mlb.FetchScheduleCached,
		Hitting:        mlb.FetchBulkHittingStats, Pitching: mlb.FetchBulkPitchingStats,
		CurrentOdds: espn.FetchGameLines, FutureOdds: odds.FetchGameLines,
		Now: time.Now, Location: time.Local, Input: input, Prompt: prompt,
		InputTerminal: inputTerminal, PromptTerminal: promptTerminal, Mode: mode,
		CurrentTeamKey: optionalCurrentTeamKey(config.Read), DebugOutput: prompt, MeasureNow: time.Now,
	}, nil
}

func optionalCurrentTeamKey(read func() (config.Config, error)) string {
	settings, err := read()
	if err != nil {
		return ""
	}
	return settings.CurrentTeamKey
}

// Close releases the service's dedicated database connection.
func (service *MLBService) Close() error {
	if service == nil || service.Store == nil {
		return nil
	}
	return service.Store.Close()
}

// Teams acquires and renders one or all 40-man rosters.
func (service *MLBService) Teams(query string, force bool) (string, error) {
	if err := service.validate("team"); err != nil {
		return "", err
	}
	now := service.Now()
	season := int64(now.In(service.location()).Year())
	teams, _, directoryRefreshed, err := cached(service.Store, now, "mlb_team_directory", "mlb", strconv.FormatInt(season, 10), directoryTTL, force, func() ([]domain.Team, error) {
		rows, err := service.Directory(season)
		if err != nil {
			return nil, err
		}
		result := make([]domain.Team, 0, len(rows))
		for _, row := range rows {
			result = append(result, teamFromProvider(row))
		}
		return result, nil
	})
	if err != nil {
		return "", commandError("team", "load team directory", err)
	}
	selected, err := service.resolveTeams(teams, query)
	if err != nil {
		return "", err
	}
	games := service.cachedSchedule(now.In(service.location()).Format("2006-01-02"), force)
	records := map[int64][2]int64{}
	standingRows, _, recordsRefreshed, standingErr := cached(service.Store, now, "mlb_team_records", "mlb", strconv.FormatInt(season, 10), totalsTTL, force, func() ([]providers.TeamStanding, error) {
		return service.Standings(season)
	})
	if standingErr == nil {
		for _, row := range standingRows {
			records[row.TeamID] = [2]int64{row.Wins, row.Losses}
		}
	}
	warnings := []string{}
	ownershipAt, ownershipErr := service.Store.OwnershipSyncedAt()
	if ownershipErr != nil {
		return "", commandError("team", "read ownership freshness", ownershipErr)
	}
	if ownershipAt == nil || now.Sub(time.Unix(*ownershipAt, 0)) >= 24*time.Hour {
		age := "never"
		if ownershipAt != nil {
			days := max(int(now.Sub(time.Unix(*ownershipAt, 0)).Hours()/24), 0)
			age = fmt.Sprintf("%dd ago", days)
		}
		warnings = append(warnings, fmt.Sprintf("OWNER data last synced %s — run `skout sync` to refresh.", age))
	}
	groups := []display.RosterGroup{}
	refreshed := directoryRefreshed || recordsRefreshed
	for _, club := range selected {
		scope := fmt.Sprintf("%d:%s", season, club.Abbreviation)
		players, stale, rosterRefreshed, rosterErr := cached(service.Store, now, "mlb_team_roster", "mlb", scope, rosterTTL, force, func() ([]domain.RosterPlayer, error) {
			rows, err := service.Roster(club.ID)
			if err != nil {
				return nil, err
			}
			result := make([]domain.RosterPlayer, 0, len(rows))
			for _, row := range rows {
				result = append(result, domain.RosterPlayer{TeamAbbreviation: club.Abbreviation, MLBAMID: row.PersonID, Name: row.FullName, Position: row.Position, PrimaryType: row.PrimaryType, Status: row.Status, JerseyNumber: row.JerseyNumber})
			}
			return result, nil
		})
		if rosterErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s roster unavailable: %v", club.Abbreviation, rosterErr))
			continue
		}
		refreshed = refreshed || rosterRefreshed
		writes := make([]store.RosterWrite, 0, len(players))
		for _, player := range players {
			writes = append(writes, store.RosterWrite{MLBAMID: player.MLBAMID, Name: player.Name, Position: player.Position, PrimaryType: player.PrimaryType, Status: player.Status, JerseyNumber: player.JerseyNumber})
		}
		if err := service.Store.ReplaceMLBRoster(club.Abbreviation, writes); err != nil {
			return "", commandError("team", "save roster", err)
		}
		if stale {
			warnings = append(warnings, club.Abbreviation+" roster is stale")
		}
		stored, err := service.Store.MLBRoster(club.Abbreviation)
		if err != nil {
			return "", commandError("team", "read enriched roster", err)
		}
		rendered := make([]domain.RosterPlayer, 0, len(stored))
		for _, player := range stored {
			rendered = append(rendered, rosterFromStore(club.Abbreviation, player, playerGameStatus(player.MLBAMID, player.PrimaryType, club.ID, games, teams, service.location())))
		}
		heading := fmt.Sprintf("%s - %s", club.Abbreviation, club.Name)
		if record, ok := records[club.ID]; ok {
			heading += fmt.Sprintf(" (%d-%d)", record[0], record[1])
		}
		groups = append(groups, display.RosterGroup{Heading: heading, Players: rendered})
	}
	if len(groups) == 0 {
		return "", commandError("team", "load rosters", fmt.Errorf("no requested team has usable roster data"))
	}
	if err := recordRefresh(service.Store, refreshed, "rosters", int64(len(groups))); err != nil {
		return "", err
	}
	return display.RenderRosters(groups, warnings, service.Mode), nil
}

// Totals acquires and renders league standings and club totals.
func (service *MLBService) Totals(force bool) (string, error) {
	if err := service.validate("team totals"); err != nil {
		return "", err
	}
	now := service.Now()
	season := int64(now.In(service.location()).Year())
	scope := strconv.FormatInt(season, 10)
	teams, _, directoryRefreshed, err := cached(service.Store, now, "mlb_team_directory", "mlb", scope, directoryTTL, force, func() ([]domain.Team, error) {
		rows, err := service.Directory(season)
		if err != nil {
			return nil, err
		}
		result := make([]domain.Team, 0, len(rows))
		for _, row := range rows {
			result = append(result, teamFromProvider(row))
		}
		return result, nil
	})
	if err != nil {
		return "", commandError("team totals", "load team directory", err)
	}
	snapshot, stale, totalsRefreshed, err := cached(service.Store, now, "mlb_team_totals", "mlb", scope, totalsTTL, force, func() (totalsSnapshot, error) {
		standings, err := service.Standings(season)
		if err != nil {
			return totalsSnapshot{}, err
		}
		hitting, err := service.Hitting(season, "R")
		if err != nil {
			return totalsSnapshot{}, err
		}
		pitching, err := service.Pitching(season, "R")
		if err != nil {
			return totalsSnapshot{}, err
		}
		return totalsSnapshot{Standings: joinStandings(teams, standings), Totals: aggregateTotals(teams, hitting, pitching), Writes: seasonStatWrites(teams, hitting, pitching)}, nil
	})
	if err != nil {
		return "", commandError("team totals", "load totals", err)
	}
	if len(snapshot.Writes) > 0 {
		if err := service.Store.ReplaceMLBSeasonStats(season, snapshot.Writes); err != nil {
			return "", commandError("team totals", "save season totals", err)
		}
	}
	counts, err := service.Store.MLBLocalPlayerCounts()
	if err != nil {
		return "", commandError("team totals", "read local Yahoo context", err)
	}
	for index := range snapshot.Totals {
		if values, ok := counts[snapshot.Totals[index].Team.Abbreviation]; ok {
			rostered, available := values[0], values[1]
			snapshot.Totals[index].YahooPlayers = &rostered
			snapshot.Totals[index].PlayersAvailable = &available
		}
		players, err := service.Store.MLBRoster(snapshot.Totals[index].Team.Abbreviation)
		if err != nil {
			return "", commandError("team totals", "read synchronized quality starts", err)
		}
		var qualityStarts int64
		for _, player := range players {
			if player.PrimaryType == "P" {
				qualityStarts += player.QualityStarts
			}
		}
		snapshot.Totals[index].Pitching.QualityStarts = int32(qualityStarts)
	}
	if err := recordRefresh(service.Store, directoryRefreshed || totalsRefreshed, "teams", int64(len(snapshot.Totals))); err != nil {
		return "", err
	}
	return display.RenderTotals(snapshot.Standings, snapshot.Totals, stale, service.Mode), nil
}

// Probables acquires and renders the three-day probable-pitcher slate.
func (service *MLBService) Probables(force bool) (string, error) {
	if err := service.validate("probable pitchers"); err != nil {
		return "", err
	}
	now := service.Now()
	localNow := now.In(service.location())
	today := localNow.Format("2006-01-02")
	season := int64(localNow.Year())
	teams, _, directoryRefreshed, err := cached(service.Store, now, "mlb_team_directory", "mlb", strconv.FormatInt(season, 10), directoryTTL, force, func() ([]domain.Team, error) {
		rows, err := service.Directory(season)
		if err != nil {
			return nil, err
		}
		result := make([]domain.Team, 0, len(rows))
		for _, row := range rows {
			result = append(result, teamFromProvider(row))
		}
		return result, nil
	})
	if err != nil {
		return "", commandError("probable pitchers", "load team directory", err)
	}
	dates := []string{today, localNow.AddDate(0, 0, 1).Format("2006-01-02"), localNow.AddDate(0, 0, 2).Format("2006-01-02")}
	current, currentStale, currentRefreshed, currentErr := cached(service.Store, now, "mlb_current_odds", "espn", today, currentOddsTTL, force, func() (providers.ESPNSlateLines, error) { return service.CurrentOdds(now) })
	if currentErr != nil {
		current = providers.ESPNSlateLines{}
		currentStale = true
		currentRefreshed = false
	}
	future := map[string][]providers.OddsSharkGameLine{}
	oddsStale, oddsRefreshed := currentStale, currentRefreshed
	for _, date := range dates[1:] {
		lines, stale, refreshed, fetchErr := cached(service.Store, now, "mlb_future_odds", "oddsshark", date, futureOddsTTL, force, func() ([]providers.OddsSharkGameLine, error) { return service.FutureOdds(date) })
		if fetchErr != nil {
			oddsStale = true
			continue
		}
		future[date] = lines
		oddsStale = oddsStale || stale
		oddsRefreshed = oddsRefreshed || refreshed
	}
	rows, stale, slateRefreshed, err := cached(service.Store, now, "mlb_probable_pitchers", "mlb", today, slateTTL, force, func() ([]domain.SlateRow, error) {
		games := []providers.ScheduleGame{}
		official := map[int64]string{}
		for _, date := range dates {
			scheduled, err := service.Schedule(date)
			if err != nil {
				return nil, err
			}
			games = append(games, scheduled...)
			for _, game := range scheduled {
				official[game.GameID] = date
			}
		}
		return composeSlate(games, teams, today, official, current, future, service.location()), nil
	})
	if err != nil {
		return "", commandError("probable pitchers", "load slate", err)
	}
	ownership, err := service.Store.MLBLocalPitcherOwnership(service.CurrentTeamKey)
	if err != nil {
		return "", commandError("probable pitchers", "read local ownership", err)
	}
	for index := range rows {
		if value, ok := ownership[strings.ToLower(rows[index].AwayPitcher)]; ok {
			rows[index].AwayFreeAgent = value[0] && !value[1]
			rows[index].AwayMine = value[2]
		}
		if value, ok := ownership[strings.ToLower(rows[index].HomePitcher)]; ok {
			rows[index].HomeFreeAgent = value[0] && !value[1]
			rows[index].HomeMine = value[2]
		}
	}
	warnings := []string{}
	if stale {
		warnings = append(warnings, "probable-pitcher slate is stale after MLB provider degradation")
	}
	if oddsStale {
		warnings = append(warnings, "odds are stale or unavailable after provider degradation")
	}
	if err := recordRefresh(service.Store, directoryRefreshed || slateRefreshed || oddsRefreshed, "slate_rows", int64(len(rows))); err != nil {
		return "", err
	}
	return display.RenderSlate(rows, warnings, service.Mode), nil
}

func (service *MLBService) validate(command string) error {
	if service == nil || service.Store == nil || service.Directory == nil || service.Standings == nil || service.Roster == nil || service.Schedule == nil || service.Hitting == nil || service.Pitching == nil || service.CurrentOdds == nil || service.FutureOdds == nil || service.Now == nil {
		return commandError(command, "initialize runtime", fmt.Errorf("an injected dependency is missing"))
	}
	return nil
}

func (service *MLBService) location() *time.Location {
	if service.Location != nil {
		return service.Location
	}
	return time.UTC
}

func (service *MLBService) cachedSchedule(date string, force bool) []providers.ScheduleGame {
	started := service.measureNow()
	if !force && service.Cache != nil && service.ScheduleCached != nil {
		if result, err := service.ScheduleCached(date, service.Cache); err == nil {
			service.reportElapsed("mlb schedule fetch", started, cacheStateName(result.CacheState))
			return result.Games
		}
	}
	games, _ := service.Schedule(date)
	service.reportElapsed("mlb schedule fetch", started, "uncached")
	return games
}

func (service *MLBService) measureNow() time.Time {
	if service.MeasureNow != nil {
		return service.MeasureNow()
	}
	return time.Now()
}

func (service *MLBService) reportElapsed(label string, started time.Time, detail string) {
	if !service.Debug || service.DebugOutput == nil {
		return
	}
	elapsed := max(service.measureNow().Sub(started), 0)
	fmt.Fprintf(service.DebugOutput, "skout debug: %s took %s (%s)\n", label, elapsed, detail)
}

func cacheStateName(state cache.State) string {
	switch state {
	case cache.Hit:
		return "Hit"
	case cache.Missing:
		return "Miss"
	case cache.Expired:
		return "Expired"
	case cache.Corrupt:
		return "Corrupt"
	default:
		return "Unknown"
	}
}

func (service *MLBService) resolveTeams(teams []domain.Team, query string) ([]domain.Team, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]domain.Team(nil), teams...), nil
	}
	folded := strings.ToLower(query)
	for _, team := range teams {
		if strings.EqualFold(team.Abbreviation, query) {
			return []domain.Team{team}, nil
		}
	}
	matches := []domain.Team{}
	for _, team := range teams {
		if strings.Contains(strings.ToLower(team.Location), folded) || strings.Contains(strings.ToLower(team.ClubName), folded) {
			matches = append(matches, team)
		}
	}
	if len(matches) == 1 {
		return matches, nil
	}
	if len(matches) == 0 {
		return nil, commandError("team", "resolve team", fmt.Errorf("no MLB club matches %q", query))
	}
	if !service.InputTerminal || !service.PromptTerminal || service.Input == nil || service.Prompt == nil {
		abbreviations := make([]string, 0, len(matches))
		for _, team := range matches {
			abbreviations = append(abbreviations, team.Abbreviation)
		}
		return nil, commandError("team", "resolve team", fmt.Errorf("%q is ambiguous; matches: %s", query, strings.Join(abbreviations, ", ")))
	}
	fmt.Fprintf(service.Prompt, "team: %q matches multiple MLB clubs:\n", query)
	for index, team := range matches {
		fmt.Fprintf(service.Prompt, "  %d) %s — %s\n", index+1, team.Abbreviation, team.Name)
	}
	fmt.Fprintf(service.Prompt, "Select a team [1-%d]: ", len(matches))
	if flusher, ok := service.Prompt.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return nil, commandError("team", "prompt for team", err)
		}
	}
	answer, err := bufio.NewReader(service.Input).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, commandError("team", "read team selection", err)
	}
	selection, parseErr := strconv.Atoi(strings.TrimSpace(answer))
	if parseErr != nil || selection < 1 || selection > len(matches) {
		return nil, commandError("team", "resolve team", fmt.Errorf("invalid selection; enter a number from 1 through %d", len(matches)))
	}
	return []domain.Team{matches[selection-1]}, nil
}

func cached[T any](database *store.Store, now time.Time, dataset, source, scope string, ttl time.Duration, force bool, fetch func() (T, error)) (T, bool, bool, error) {
	var zero T
	previous, err := database.CommandSnapshot(dataset, source, scope)
	if err != nil {
		return zero, false, false, err
	}
	if !force && previous != nil && !previous.Stale && now.Sub(previous.LastSuccessfulAt) < ttl {
		var value T
		if json.Unmarshal([]byte(previous.Payload), &value) == nil {
			return value, false, false, nil
		}
	}
	value, fetchErr := fetch()
	if fetchErr == nil {
		payload, err := json.Marshal(value)
		if err != nil {
			return zero, false, false, fmt.Errorf("encode snapshot: %w", err)
		}
		if err := database.SaveCommandSnapshot(dataset, source, scope, "v1", string(payload)); err != nil {
			return zero, false, false, err
		}
		return value, false, true, nil
	}
	if previous == nil {
		return zero, false, false, fetchErr
	}
	_, _ = database.MarkCommandSnapshotStale(dataset, source, scope, fetchErr.Error())
	if err := json.Unmarshal([]byte(previous.Payload), &zero); err != nil {
		return zero, true, false, fmt.Errorf("decode stale snapshot: %w", err)
	}
	return zero, true, false, nil
}

type totalsSnapshot struct {
	Standings []domain.Standing
	Totals    []domain.TeamTotals
	Writes    []store.SeasonStatWrite
}

func (snapshot totalsSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{snapshot.Standings, snapshot.Totals, snapshot.Writes})
}
func (snapshot *totalsSnapshot) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("totals snapshot must contain three arrays")
	}
	if err := json.Unmarshal(parts[0], &snapshot.Standings); err != nil {
		return err
	}
	if err := json.Unmarshal(parts[1], &snapshot.Totals); err != nil {
		return err
	}
	return json.Unmarshal(parts[2], &snapshot.Writes)
}

func teamFromProvider(row providers.TeamDirectoryEntry) domain.Team {
	return domain.Team{ID: row.TeamID, Name: row.Name, Location: row.LocationName, ClubName: row.ClubName, Abbreviation: row.Abbreviation, LeagueID: row.LeagueID}
}

func rosterFromStore(team string, row store.StoredRosterPlayer, gameStatus string) domain.RosterPlayer {
	return domain.RosterPlayer{TeamAbbreviation: team, MLBAMID: row.MLBAMID, Name: row.Name, Position: row.Position, PrimaryType: row.PrimaryType, Status: row.Status, InjuryStatus: row.InjuryStatus, GameStatus: gameStatus, IsCloser: row.IsCloser, JerseyNumber: row.JerseyNumber, EligiblePositions: row.EligiblePositions, BatSide: row.BatSide, PitchHand: row.PitchHand, YahooRank: row.YahooRank, Owner: row.Owner, InYahooPool: row.InYahooPool, PlateAppearances: row.PlateAppearances, OnBasePercentage: row.OnBasePercentage, Runs: row.Runs, HomeRuns: row.HomeRuns, RunsBattedIn: row.RunsBattedIn, StolenBases: row.StolenBases, BattingAverage: row.BattingAverage, InningsPitched: row.InningsPitched, QualityStarts: row.QualityStarts, Wins: row.Wins, Saves: row.Saves, Strikeouts: row.Strikeouts, EarnedRunAverage: row.EarnedRunAverage, WHIP: row.WHIP}
}

func joinStandings(teams []domain.Team, rows []providers.TeamStanding) []domain.Standing {
	byID := map[int64]domain.Team{}
	for _, team := range teams {
		byID[team.ID] = team
	}
	result := []domain.Standing{}
	for _, row := range rows {
		if team, ok := byID[row.TeamID]; ok {
			result = append(result, domain.Standing{Team: team, Wins: row.Wins, Losses: row.Losses, GamesBack: row.GamesBack})
		}
	}
	return result
}

func aggregateTotals(teams []domain.Team, hitting []providers.BulkHittingSplit, pitching []providers.BulkPitchingSplit) []domain.TeamTotals {
	result := make([]domain.TeamTotals, 0, len(teams))
	for _, team := range teams {
		var ab, hits, walks, hitByPitch, totalBases, plateAppearances, homeRuns, runsBattedIn, runs, stolenBases, hittingStrikeouts int64
		for _, row := range hitting {
			if row.Team.TeamID == team.ID {
				stat := row.Stat
				ab += stat.AtBats
				hits += stat.Hits
				walks += stat.Walks
				hitByPitch += stat.HitByPitch
				totalBases += stat.TotalBases
				plateAppearances += stat.PlateAppearances
				homeRuns += stat.HomeRuns
				runsBattedIn += stat.RBI
				runs += stat.Runs
				stolenBases += stat.StolenBases
				hittingStrikeouts += stat.Strikeouts
			}
		}
		var games, gamesStarted, outs, earnedRuns, hitsAllowed, pitchingWalks, pitchingStrikeouts, wins, saves, holds, qualityStarts int64
		for _, row := range pitching {
			if row.Team.TeamID == team.ID {
				stat := row.Stat
				games += stat.GamesPitched
				gamesStarted += stat.GamesStarted
				outs += inningsOuts(stat.InningsPitched)
				earnedRuns += stat.EarnedRuns
				hitsAllowed += stat.HitsAllowed
				pitchingWalks += stat.Walks
				pitchingStrikeouts += stat.Strikeouts
				wins += stat.Wins
				saves += stat.Saves
				holds += stat.Holds
				qualityStarts += stat.QualityStarts
			}
		}
		innings := float64(outs) / 3
		battingAverage, obp, slugging := divide(hits, ab), divide(hits+walks+hitByPitch, ab+walks+hitByPitch), divide(totalBases, ab)
		era, whip := 0.0, 0.0
		if innings != 0 {
			era = 9 * float64(earnedRuns) / innings
			whip = float64(hitsAllowed+pitchingWalks) / innings
		}
		result = append(result, domain.TeamTotals{Team: team, Batting: domain.BattingStats{PlateAppearances: int32(plateAppearances), BattingAverage: battingAverage, OnBasePercentage: obp, SluggingPercentage: slugging, OnBasePlusSlugging: obp + slugging, HomeRuns: int32(homeRuns), RunsBattedIn: int32(runsBattedIn), Runs: int32(runs), StolenBases: int32(stolenBases), Strikeouts: int32(hittingStrikeouts), Walks: int32(walks)}, Pitching: domain.PitchingStats{Games: int32(games), GamesStarted: int32(gamesStarted), InningsPitched: innings, EarnedRunAverage: era, WHIP: whip, Strikeouts: int32(pitchingStrikeouts), Wins: int32(wins), Saves: int32(saves), Holds: int32(holds), QualityStarts: int32(qualityStarts)}})
	}
	return result
}

func seasonStatWrites(teams []domain.Team, hitting []providers.BulkHittingSplit, pitching []providers.BulkPitchingSplit) []store.SeasonStatWrite {
	abbreviation := map[int64]string{}
	for _, team := range teams {
		abbreviation[team.ID] = team.Abbreviation
	}
	rows := []store.SeasonStatWrite{}
	for _, row := range hitting {
		if team, ok := abbreviation[row.Team.TeamID]; ok {
			stat := row.Stat
			rows = append(rows, store.SeasonStatWrite{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, TeamAbbreviation: team, StatGroup: "hitting", Games: stat.GamesPlayed, PlateAppearances: stat.PlateAppearances, AtBats: stat.AtBats, Hits: stat.Hits, HomeRuns: stat.HomeRuns, RunsBattedIn: stat.RBI, Runs: stat.Runs, StolenBases: stat.StolenBases, Walks: stat.Walks, HitByPitch: stat.HitByPitch, TotalBases: stat.TotalBases})
		}
	}
	for _, row := range pitching {
		if team, ok := abbreviation[row.Team.TeamID]; ok {
			stat := row.Stat
			rows = append(rows, store.SeasonStatWrite{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, TeamAbbreviation: team, StatGroup: "pitching", Games: stat.GamesPitched, Wins: stat.Wins, Saves: stat.Saves, Holds: stat.Holds, Strikeouts: stat.Strikeouts, InningsOuts: inningsOuts(stat.InningsPitched), GamesStarted: stat.GamesStarted, QualityStarts: stat.QualityStarts, HitsAllowed: stat.HitsAllowed, EarnedRuns: stat.EarnedRuns, PitcherWalks: stat.Walks})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].MLBAMID != rows[j].MLBAMID {
			return rows[i].MLBAMID < rows[j].MLBAMID
		}
		return rows[i].StatGroup < rows[j].StatGroup
	})
	return rows
}

func composeSlate(games []providers.ScheduleGame, teams []domain.Team, today string, official map[int64]string, current providers.ESPNSlateLines, future map[string][]providers.OddsSharkGameLine, location *time.Location) []domain.SlateRow {
	names := map[int64]string{}
	for _, team := range teams {
		names[team.ID] = team.Abbreviation
	}
	orderedGames := append([]providers.ScheduleGame(nil), games...)
	sort.SliceStable(orderedGames, func(i, j int) bool {
		if orderedGames[i].GameDate != orderedGames[j].GameDate {
			return orderedGames[i].GameDate < orderedGames[j].GameDate
		}
		return orderedGames[i].GameID < orderedGames[j].GameID
	})
	rows := []domain.SlateRow{}
	for _, game := range orderedGames {
		date := gameOfficialDate(game, official)
		occurrence := 0
		for _, previous := range games {
			if previous.GameID == game.GameID {
				break
			}
			if gameOfficialDate(previous, official) == date && sameTeam(previous.AwayTeamName, game.AwayTeamName) && sameTeam(previous.HomeTeamName, game.HomeTeamName) {
				occurrence++
			}
		}
		var probability *float64
		if date == today {
			matched := 0
			for _, line := range current.Games {
				if line.Quoted && sameTeam(line.AwayTeam, game.AwayTeamName) && sameTeam(line.HomeTeam, game.HomeTeamName) {
					if matched == occurrence {
						away, _ := normalizedProbability(line.AwayMoneyline, line.HomeMoneyline)
						probability = &away
						break
					}
					matched++
				}
			}
		} else {
			var selected *providers.OddsSharkGameLine
			for i := range future[date] {
				line := &future[date][i]
				if line.EventID == strconv.FormatInt(game.GameID, 10) || line.StartTime != "" && line.StartTime == game.GameDate {
					selected = line
					break
				}
			}
			if selected == nil {
				matched := 0
				for i := range future[date] {
					line := &future[date][i]
					if sameTeam(line.AwayTeam, game.AwayTeamName) && sameTeam(line.HomeTeam, game.HomeTeamName) {
						if matched == occurrence {
							selected = line
							break
						}
						matched++
					}
				}
			}
			if selected != nil {
				away, _ := normalizedProbability(selected.AwayMoneyline, selected.HomeMoneyline)
				probability = &away
			}
		}
		awayName, homeName := game.AwayProbablePitcherName, game.HomeProbablePitcherName
		if strings.TrimSpace(awayName) == "" {
			awayName = "TBD"
		}
		if strings.TrimSpace(homeName) == "" {
			homeName = "TBD"
		}
		awayTeam, homeTeam := names[game.AwayTeamID], names[game.HomeTeamID]
		if awayTeam == "" {
			awayTeam = game.AwayTeamName
		}
		if homeTeam == "" {
			homeTeam = game.HomeTeamName
		}
		rows = append(rows, domain.SlateRow{Date: compactDate(date, location), GameID: game.GameID, GameTime: localGameTime(game.GameDate, location), AwayTeam: awayTeam, HomeTeam: homeTeam, AwayPitcher: awayName, HomePitcher: homeName, WinProbability: probability})
	}
	return rows
}

func gameOfficialDate(game providers.ScheduleGame, official map[int64]string) string {
	if date, exists := official[game.GameID]; exists {
		return date
	}
	runes := []rune(game.GameDate)
	return string(runes[:min(10, len(runes))])
}

func playerGameStatus(mlbamID int64, role string, teamID int64, games []providers.ScheduleGame, teams []domain.Team, location *time.Location) string {
	abbreviation := map[int64]string{}
	for _, team := range teams {
		abbreviation[team.ID] = team.Abbreviation
	}
	for _, game := range games {
		if game.AwayTeamID != teamID && game.HomeTeamID != teamID {
			continue
		}
		away := game.AwayTeamID == teamID
		opponentID, marker := game.AwayTeamID, "v"
		if away {
			opponentID, marker = game.HomeTeamID, "@"
		}
		opponent := abbreviation[opponentID]
		if opponent == "" {
			opponent = "—"
		}
		state := strings.ToLower(game.DetailedState)
		if state == "final" {
			return fmt.Sprintf("Final %s %s", marker, opponent)
		}
		if state != "scheduled" && state != "pre-game" && state != "pregame" && state != "warmup" {
			return fmt.Sprintf("Live %s %s", marker, opponent)
		}
		indicator := ""
		if role == "P" {
			probable := game.HomeProbablePitcherID
			if away {
				probable = game.AwayProbablePitcherID
			}
			if probable != nil && *probable == mlbamID {
				indicator = "●"
			}
		} else {
			lineup := game.HomeLineup
			if away {
				lineup = game.AwayLineup
			}
			for index, player := range lineup {
				if player.PersonID == mlbamID {
					indicator = strconv.Itoa(index + 1)
					break
				}
			}
			if indicator == "" && len(lineup) > 0 {
				indicator = "●"
			}
		}
		return fmt.Sprintf("%s %-1s %s %s", localGameTime(game.GameDate, location), indicator, marker, opponent)
	}
	return ""
}

func localGameTime(value string, location *time.Location) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimLeft(parsed.In(location).Format("03:04pm"), "0")), "m")
}
func compactDate(value string, location *time.Location) string {
	parsed, err := time.ParseInLocation("2006-01-02", value, location)
	if err != nil {
		return value
	}
	return parsed.Format("Jan 02 Mon")
}
func sameTeam(left, right string) bool {
	fold := func(value string) string {
		var output strings.Builder
		for _, character := range strings.ToLower(value) {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
				output.WriteRune(character)
			}
		}
		return output.String()
	}
	return fold(left) == fold(right)
}
func normalizedProbability(away, home int64) (float64, float64) {
	implied := func(value int64) float64 {
		if value > 0 {
			return 100 / (float64(value) + 100)
		}
		if value < 0 {
			return float64(-value) / (float64(-value) + 100)
		}
		return 0
	}
	a, h := implied(away), implied(home)
	total := a + h
	if total == 0 {
		return 0, 0
	}
	return a / total, h / total
}
func inningsOuts(value string) int64 {
	parts := strings.SplitN(value, ".", 2)
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	remainder := int64(0)
	if len(parts) == 2 {
		remainder, _ = strconv.ParseInt(parts[1], 10, 64)
		if remainder > 2 {
			remainder = 2
		}
	}
	return whole*3 + remainder
}
func divide(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
func commandError(command, operation string, err error) error {
	return fmt.Errorf("%s: %s: %v; verify connectivity and retry", command, operation, err)
}
func recordRefresh(database *store.Store, refreshed bool, key string, count int64) error {
	if !refreshed {
		return nil
	}
	id, err := database.StartSyncRun(store.SyncLive, store.OriginManual)
	if err != nil {
		return commandError("mlb", "start foreground refresh record", err)
	}
	if _, err := database.CompleteSyncRun(id, map[string]int64{key: count}); err != nil {
		return commandError("mlb", "complete foreground refresh record", err)
	}
	return nil
}
