package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// liveReuseWindow bounds how long live command context — the Yahoo matchup
// view, weekly scoreboard, roster slots, ESPN odds, daily MLB overlay, and
// player game logs — is reused before refetching.
const liveReuseWindow = 5 * time.Minute

type yahooMatchupError struct{ err error }

func (failure *yahooMatchupError) Error() string { return failure.err.Error() }
func (failure *yahooMatchupError) Unwrap() error { return failure.err }

func yahooUnavailable(err error) error { return &yahooMatchupError{err: err} }

// MatchupOptions selects one frozen daily or weekly matchup surface.
type MatchupOptions struct {
	Team   string
	Week   int
	Weekly bool
	Day    string
}

// MatchupService orchestrates public Yahoo matchup acquisition and fallback.
type MatchupService struct {
	Store          *store.Store
	League         string
	TeamKey        string
	Season         int
	ArchivedSeason int
	FetchRedzone   func(string, string) (providers.RedzoneFeed, error)
	Scoreboard     func(string, *int) ([]domain.Matchup, error)
	RosterWeek     func(string, int) (domain.RosterWeekStats, error)
	RosterDay      func(string, int, string) (domain.RosterWeekStats, error)
	HittingRange   func(int64, string, string) ([]providers.BulkHittingSplit, error)
	PitchingRange  func(int64, string, string) ([]providers.BulkPitchingSplit, error)
	Schedule       func(string) ([]providers.ScheduleGame, error)
	Odds           func(time.Time) (providers.ESPNSlateLines, error)
	PersistTeam    func(string) error
	AutoSync       func() error
	Now            func() time.Time
	Mode           terminal.ColorMode
}

// ensureFreshYahoo runs one blocking sync when the last successful Yahoo sync
// is missing or older than the fantasy freshness threshold.
func (service *MatchupService) ensureFreshYahoo() error {
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
	return fmt.Errorf("match: Yahoo sync failed (%v) and no prior sync data exists; check connectivity and run skout sync", syncErr)
}

// NewProductionMatchupService opens public providers and compatible local state.
func NewProductionMatchupService(version, leagueOverride string, seasonOverride int, mode terminal.ColorMode) (*MatchupService, error) {
	settings, err := config.Read()
	if err != nil {
		return nil, fmt.Errorf("match: read configuration: %w", err)
	}
	league := strings.TrimSpace(leagueOverride)
	overridden := league != ""
	if league == "" {
		league = strings.TrimSpace(settings.CurrentLeague)
	}
	if league == "" && seasonOverride == 0 {
		return nil, fmt.Errorf("match: no league selected; run skout st -l <key>")
	}
	database, err := store.Open()
	if err != nil {
		return nil, fmt.Errorf("match: open database: %w", err)
	}
	if seasonOverride > 0 {
		key, live, seasonErr := seasonScopedLeague(database, league, seasonOverride)
		if seasonErr != nil {
			_ = database.Close()
			return nil, fmt.Errorf("match: %w", seasonErr)
		}
		if !live {
			return &MatchupService{
				Store: database, League: key, TeamKey: settings.CurrentTeamKey,
				Season: seasonOverride, ArchivedSeason: seasonOverride,
				PersistTeam: func(string) error { return nil },
				Now:         time.Now, Mode: mode,
			}, nil
		}
	}
	season, err := database.FantasySeason(league)
	if err != nil || season == nil {
		_ = database.Close()
		if err == nil {
			err = fmt.Errorf("league season is unavailable")
		}
		return nil, fmt.Errorf("match: read league season: %w", err)
	}
	disk, err := cache.Production()
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("match: initialize cache: %w", err)
	}
	http := transport.Production()
	yahoo := providers.NewProductionYahooPublicClient(http)
	mlb := providers.NewProductionMLBClient(http)
	espn := providers.NewProductionESPNClient(http, version)
	return &MatchupService{
		Store: database, League: league, TeamKey: settings.CurrentTeamKey, Season: *season,
		FetchRedzone: yahoo.FetchRedzone, Scoreboard: yahoo.Scoreboard,
		RosterWeek: yahoo.RosterWeekStats, RosterDay: yahoo.RosterDayStats,
		HittingRange: mlb.FetchHittingStatsByDateRange, PitchingRange: mlb.FetchPitchingStatsByDateRange,
		Schedule: func(date string) ([]providers.ScheduleGame, error) {
			result, fetchErr := mlb.FetchScheduleCached(date, disk)
			return result.Games, fetchErr
		},
		Odds:        espn.FetchGameLines,
		PersistTeam: productionMatchupTeamPersister(overridden),
		Now:         time.Now, Mode: mode,
	}, nil
}

func productionMatchupTeamPersister(overridden bool) func(string) error {
	if overridden {
		return func(string) error { return nil }
	}
	return func(teamKey string) error {
		current, err := config.Read()
		if err != nil {
			return err
		}
		current.CurrentTeamKey = teamKey
		return config.Write(current)
	}
}

// Close releases the matchup service store.
func (service *MatchupService) Close() error {
	if service == nil || service.Store == nil {
		return nil
	}
	return service.Store.Close()
}

// Matchup resolves, acquires, enriches, and renders one matchup invocation.
func (service *MatchupService) Matchup(options MatchupOptions) (string, error) {
	if service == nil || service.Store == nil || service.League == "" {
		return "", fmt.Errorf("match: runtime boundaries are incomplete; reinstall skout")
	}
	if options.Week < 0 || options.Week > 0 && (options.Weekly || options.Day != "") || options.Weekly && options.Day != "" {
		return "", fmt.Errorf("match: select only one period")
	}
	if service.ArchivedSeason > 0 {
		if options.Day != "" {
			return "", fmt.Errorf("match: the daily view is unavailable for an archived season; use --week or --weekly")
		}
		if options.Week == 0 {
			options.Weekly = true
		}
	}
	if err := service.ensureFreshYahoo(); err != nil {
		return "", err
	}
	teams, err := service.Store.FantasyTeams(service.League)
	if err != nil {
		return "", fantasyError("match", "read teams", err)
	}
	selected, err := selectFantasyTeam(teams, service.TeamKey, options.Team)
	if err != nil {
		return "", fmt.Errorf("match: %w", service.archivedMatchupTeamHint(teams, err))
	}
	changedTeam := options.Team != "" && selected.TeamKey != service.TeamKey
	if changedTeam && service.PersistTeam == nil {
		return "", fmt.Errorf("match: save selected team: configuration writer is unavailable")
	}
	current, err := service.Store.FantasyCurrentWeek(service.League)
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("league current week is unavailable")
		}
		return "", fantasyError("match", "read current week", err)
	}
	if options.Week > *current {
		return "", fmt.Errorf("match: week %d is in the future; choose week %d or earlier", options.Week, *current)
	}
	period := matchupPeriod{week: *current, scope: "current", day: service.now().Format("2006-01-02"), daily: !options.Weekly && options.Week == 0 && options.Day == "", gameContext: true}
	if options.Week > 0 {
		period.week, period.scope, period.daily = options.Week, strconv.Itoa(options.Week), false
		period.preferSnapshot, period.gameContext = options.Week < *current, options.Week == *current
	} else if options.Weekly {
		period.scope, period.daily = strconv.Itoa(*current), false
	} else if options.Day != "" {
		day, dayErr := resolveSeasonDay(options.Day, service.Season)
		if dayErr != nil {
			return "", dayErr
		}
		period.day, period.scope, period.daily = day, strconv.Itoa(*current), true
		period.week, err = service.weekForDay(day, *current, selected.TeamKey)
		if err != nil {
			return "", err
		}
		period.preferSnapshot = true
	}
	if service.ArchivedSeason > 0 {
		period.preferSnapshot, period.gameContext, period.daily = true, false, false
	}
	view, err := service.acquire(period, selected, teams, service.startPrefetch(period))
	var output string
	if err == nil {
		output = display.RenderMatchup(view, service.Mode)
	} else {
		if _, ok := errors.AsType[*yahooMatchupError](err); !ok {
			return "", err
		}
		local, fallbackErr := service.localFallback(selected)
		if fallbackErr != nil {
			return "", fmt.Errorf("match: refresh Yahoo matchup: %v; %w", err, fallbackErr)
		}
		output = display.RenderLocalMatchup(local, service.Mode)
	}
	if service.ArchivedSeason > 0 {
		output = display.ArchivedNotice(service.ArchivedSeason, service.Mode) + output
	}
	if changedTeam {
		if err := service.PersistTeam(selected.TeamKey); err != nil {
			return "", fantasyError("match", "save selected team", err)
		}
		service.TeamKey = selected.TeamKey
	}
	return output, nil
}

// matchupFantasyPlayers reads league players, pinning stats to the archived season when one is selected.
func (service *MatchupService) matchupFantasyPlayers() ([]domain.StoredFantasyPlayer, error) {
	if service.ArchivedSeason > 0 {
		return service.Store.FantasyPlayersForSeason(service.League, service.ArchivedSeason)
	}
	return service.Store.FantasyPlayers(service.League)
}

// archivedMatchupTeamHint appends the archived season's team list to a failed team match.
func (service *MatchupService) archivedMatchupTeamHint(teams []store.StoredFantasyTeam, err error) error {
	if service.ArchivedSeason == 0 || len(teams) == 0 {
		return err
	}
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		names = append(names, team.Name)
	}
	return fmt.Errorf("%v (archived season %d teams: %s)", err, service.ArchivedSeason, strings.Join(names, ", "))
}

type matchupPeriod struct {
	week           int
	scope          string
	day            string
	daily          bool
	preferSnapshot bool
	gameContext    bool
}

type matchupScheduleResult struct {
	games []providers.ScheduleGame
	err   error
}

type matchupOddsResult struct {
	lines providers.ESPNSlateLines
	err   error
}

// matchupPrefetch carries schedule and odds fetches started concurrently with
// the Yahoo matchup acquisition.
type matchupPrefetch struct {
	schedule chan matchupScheduleResult
	odds     chan matchupOddsResult
}

// startPrefetch begins the game-context provider fetches so they overlap the
// Yahoo matchup fetch instead of running serially after it.
func (service *MatchupService) startPrefetch(period matchupPeriod) *matchupPrefetch {
	prefetch := &matchupPrefetch{}
	if !period.gameContext || service.Schedule == nil {
		return prefetch
	}
	day := period.day
	prefetch.schedule = make(chan matchupScheduleResult, 1)
	go func() {
		games, err := service.Schedule(day)
		prefetch.schedule <- matchupScheduleResult{games: games, err: err}
	}()
	if service.Odds != nil {
		prefetch.odds = make(chan matchupOddsResult, 1)
		if lines, ok := service.storedOddsWithinWindow(day); ok {
			prefetch.odds <- matchupOddsResult{lines: lines}
		} else {
			go func() {
				lines, err := service.Odds(service.now())
				if err == nil {
					service.saveOddsSnapshot(day, lines)
				}
				prefetch.odds <- matchupOddsResult{lines: lines, err: err}
			}()
		}
	}
	return prefetch
}

// storedOddsWithinWindow serves the day's odds snapshot while it is inside the
// matchup reuse window.
func (service *MatchupService) storedOddsWithinWindow(day string) (providers.ESPNSlateLines, bool) {
	snapshot, err := service.Store.CommandSnapshot("match_odds", "espn", day)
	if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" || snapshot.Stale || service.now().Sub(snapshot.LastSuccessfulAt) > liveReuseWindow {
		return providers.ESPNSlateLines{}, false
	}
	var lines providers.ESPNSlateLines
	if json.Unmarshal([]byte(snapshot.Payload), &lines) != nil {
		return providers.ESPNSlateLines{}, false
	}
	return lines, true
}

func (service *MatchupService) saveOddsSnapshot(day string, lines providers.ESPNSlateLines) {
	if payload, err := json.Marshal(lines); err == nil {
		_ = service.Store.SaveCommandSnapshot("match_odds", "espn", day, "v1", string(payload))
	}
}

func (service *MatchupService) acquire(period matchupPeriod, selected store.StoredFantasyTeam, storedTeams []store.StoredFantasyTeam, prefetch *matchupPrefetch) (domain.MatchupView, error) {
	if period.scope == "current" {
		if view, ok := service.freshDefault(period, selected, storedTeams); ok {
			return service.enrichView(view, period, prefetch)
		}
		leagueID, err := providers.LeagueIDFromKey(service.League)
		if err != nil {
			return domain.MatchupView{}, err
		}
		if service.FetchRedzone == nil {
			return domain.MatchupView{}, yahooUnavailable(fmt.Errorf("yahoo public feed is unavailable"))
		}
		feed, err := service.FetchRedzone(leagueID, service.League)
		if err != nil {
			if view, ok := service.staleDefault(period, selected, storedTeams); ok {
				view.Stale = true
				return service.enrichView(view, period, prefetch)
			}
			return domain.MatchupView{}, yahooUnavailable(err)
		}
		matchup, mine, opponent, err := matchFeed(feed.Matchups, feed.RosterWeekStats, selected.TeamKey, period.week)
		if err != nil {
			return domain.MatchupView{}, yahooUnavailable(err)
		}
		if err := service.saveMatchupSnapshots("yahoo_public", "v1", period, matchup, mine, opponent); err != nil {
			return domain.MatchupView{}, err
		}
		return service.enrichView(domain.MatchupView{Matchup: matchup, Mine: mine, Opponent: opponent, Teams: feed.Teams}, period, prefetch)
	}
	matchups, stale, err := service.scoreboard(period.week, period.preferSnapshot)
	if err != nil {
		return domain.MatchupView{}, err
	}
	var matchup *domain.Matchup
	for index := range matchups {
		if matchups[index].Week == period.week && matchupHasTeam(matchups[index], selected.TeamKey) {
			matchup = &matchups[index]
			break
		}
	}
	if matchup == nil {
		return domain.MatchupView{}, yahooUnavailable(fmt.Errorf("selected team has no matchup for week %d", period.week))
	}
	opponentKey := matchup.Teams[0].TeamKey
	if opponentKey == selected.TeamKey {
		opponentKey = matchup.Teams[1].TeamKey
	}
	dataset := "match_roster"
	if period.day != "" && period.daily {
		dataset = "match_roster_day"
	}
	mine, mineStale, err := service.selectedRoster(dataset, selected.TeamKey, period)
	if err != nil {
		return domain.MatchupView{}, err
	}
	opponent, opponentStale, err := service.selectedRoster(dataset, opponentKey, period)
	if err != nil {
		return domain.MatchupView{}, err
	}
	view := domain.MatchupView{Matchup: *matchup, Mine: mine, Opponent: opponent, Teams: fantasyTeamsFromStore(storedTeams), Stale: stale || mineStale || opponentStale}
	return service.enrichView(view, period, prefetch)
}

func (service *MatchupService) scoreboard(week int, preferSnapshot bool) ([]domain.Matchup, bool, error) {
	scope := fmt.Sprintf("%s:%d", service.League, week)
	if preferSnapshot {
		if rows, stale, ok, err := service.scoreboardSnapshot(scope); err != nil {
			return nil, false, err
		} else if ok {
			return rows, stale, nil
		}
	}
	var refreshErr error
	if service.Scoreboard != nil {
		rows, err := service.Scoreboard(service.League, &week)
		if err == nil && validMatchupRows(rows) {
			payload, _ := json.Marshal(rows)
			if saveErr := service.Store.SaveCommandSnapshot("match_scoreboard", "yahoo", scope, "v1", string(payload)); saveErr != nil {
				return nil, false, saveErr
			}
			return rows, false, nil
		}
		refreshErr = err
		if refreshErr == nil {
			refreshErr = fmt.Errorf("scoreboard response is incomplete")
		}
		_, _ = service.Store.MarkCommandSnapshotStale("match_scoreboard", "yahoo", scope, refreshErr.Error())
	} else {
		refreshErr = fmt.Errorf("scoreboard provider is unavailable")
	}
	if rows, _, ok, err := service.scoreboardSnapshot(scope); err != nil {
		return nil, false, err
	} else if ok {
		return rows, true, nil
	}
	return nil, false, yahooUnavailable(fmt.Errorf("yahoo scoreboard unavailable (%v); run skout sync and retry", refreshErr))
}

func (service *MatchupService) scoreboardSnapshot(scope string) ([]domain.Matchup, bool, bool, error) {
	snapshot, err := service.Store.CommandSnapshot("match_scoreboard", "yahoo", scope)
	if err != nil {
		return nil, false, false, err
	}
	if snapshot == nil || snapshot.SnapshotVersion != "v1" {
		return nil, false, false, nil
	}
	var rows []domain.Matchup
	if json.Unmarshal([]byte(snapshot.Payload), &rows) != nil || !validMatchupRows(rows) {
		return nil, false, false, nil
	}
	return rows, snapshot.Stale, true, nil
}

func (service *MatchupService) selectedRoster(dataset, team string, period matchupPeriod) (domain.RosterWeekStats, bool, error) {
	scope := fmt.Sprintf("%s:%d", team, period.week)
	if dataset == "match_roster_day" {
		scope = team + ":" + period.day
	}
	if period.preferSnapshot {
		if row, stale, ok, err := service.rosterSnapshot(dataset, scope, team, period.week); err != nil {
			return domain.RosterWeekStats{}, false, err
		} else if ok {
			return row, stale, nil
		}
	}
	var row domain.RosterWeekStats
	var err error
	if dataset == "match_roster_day" && service.RosterDay != nil {
		row, err = service.RosterDay(team, period.week, period.day)
	} else if service.RosterWeek != nil {
		row, err = service.RosterWeek(team, period.week)
	} else {
		err = fmt.Errorf("yahoo roster provider is unavailable")
	}
	if err == nil && validRoster(row, team, period.week) {
		payload, _ := json.Marshal(row)
		if saveErr := service.Store.SaveCommandSnapshot(dataset, "yahoo", scope, "v2", string(payload)); saveErr != nil {
			return domain.RosterWeekStats{}, false, saveErr
		}
		return row, false, nil
	}
	if err == nil {
		err = fmt.Errorf("roster response is incomplete")
	}
	_, _ = service.Store.MarkCommandSnapshotStale(dataset, "yahoo", scope, err.Error())
	if row, _, ok, readErr := service.rosterSnapshot(dataset, scope, team, period.week); readErr != nil {
		return domain.RosterWeekStats{}, false, readErr
	} else if ok {
		return row, true, nil
	}
	return domain.RosterWeekStats{}, false, yahooUnavailable(fmt.Errorf("yahoo roster unavailable for %s (%v)", team, err))
}

func (service *MatchupService) rosterSnapshot(dataset, scope, team string, week int) (domain.RosterWeekStats, bool, bool, error) {
	snapshot, err := service.Store.CommandSnapshot(dataset, "yahoo", scope)
	if err != nil {
		return domain.RosterWeekStats{}, false, false, err
	}
	if snapshot == nil || snapshot.SnapshotVersion != "v2" {
		return domain.RosterWeekStats{}, false, false, nil
	}
	var row domain.RosterWeekStats
	if json.Unmarshal([]byte(snapshot.Payload), &row) != nil || !validRoster(row, team, week) {
		return domain.RosterWeekStats{}, false, false, nil
	}
	return row, snapshot.Stale, true, nil
}

func (service *MatchupService) freshDefault(period matchupPeriod, selected store.StoredFantasyTeam, teams []store.StoredFantasyTeam) (domain.MatchupView, bool) {
	return service.defaultSnapshots(period, selected, teams, true)
}

func (service *MatchupService) staleDefault(period matchupPeriod, selected store.StoredFantasyTeam, teams []store.StoredFantasyTeam) (domain.MatchupView, bool) {
	return service.defaultSnapshots(period, selected, teams, false)
}

func (service *MatchupService) defaultSnapshots(period matchupPeriod, selected store.StoredFantasyTeam, teams []store.StoredFantasyTeam, freshOnly bool) (domain.MatchupView, bool) {
	scope := service.League + ":current"
	rows, err := service.Store.CommandSnapshotsByScope("match_scoreboard", scope)
	if err != nil {
		return domain.MatchupView{}, false
	}
	for _, snapshot := range rows {
		if snapshot.Source != "yahoo" && snapshot.Source != "yahoo_public" || snapshot.SnapshotVersion != "v1" || freshOnly && service.now().Sub(snapshot.LastSuccessfulAt) > liveReuseWindow {
			continue
		}
		var matchups []domain.Matchup
		if json.Unmarshal([]byte(snapshot.Payload), &matchups) != nil || !validMatchupRows(matchups) {
			continue
		}
		for _, matchup := range matchups {
			if !matchupHasTeam(matchup, selected.TeamKey) {
				continue
			}
			opponentKey := matchup.Teams[0].TeamKey
			if opponentKey == selected.TeamKey {
				opponentKey = matchup.Teams[1].TeamKey
			}
			mine, mineOK := service.compatibleRoster("match_roster", selected.TeamKey+":"+strconv.Itoa(matchup.Week), selected.TeamKey, matchup.Week, freshOnly)
			opponent, opponentOK := service.compatibleRoster("match_roster", opponentKey+":"+strconv.Itoa(matchup.Week), opponentKey, matchup.Week, freshOnly)
			if mineOK && opponentOK {
				return domain.MatchupView{Matchup: matchup, Mine: mine, Opponent: opponent, Teams: fantasyTeamsFromStore(teams), Stale: snapshot.Stale}, true
			}
		}
	}
	return domain.MatchupView{}, false
}

func (service *MatchupService) compatibleRoster(dataset, scope, team string, week int, freshOnly bool) (domain.RosterWeekStats, bool) {
	rows, err := service.Store.CommandSnapshotsByScope(dataset, scope)
	if err != nil {
		return domain.RosterWeekStats{}, false
	}
	for _, snapshot := range rows {
		if snapshot.Source != "yahoo" && snapshot.Source != "yahoo_public" || snapshot.SnapshotVersion != "v1" && snapshot.SnapshotVersion != "v2" || freshOnly && service.now().Sub(snapshot.LastSuccessfulAt) > liveReuseWindow {
			continue
		}
		var row domain.RosterWeekStats
		if json.Unmarshal([]byte(snapshot.Payload), &row) == nil && validRoster(row, team, week) {
			return row, true
		}
	}
	return domain.RosterWeekStats{}, false
}

func (service *MatchupService) saveMatchupSnapshots(source, version string, period matchupPeriod, matchup domain.Matchup, mine, opponent domain.RosterWeekStats) error {
	if !validMatchupRows([]domain.Matchup{matchup}) || !validRoster(mine, mine.TeamKey, matchup.Week) || !validRoster(opponent, opponent.TeamKey, matchup.Week) {
		return fmt.Errorf("match: refusing to save an incomplete Yahoo matchup snapshot")
	}
	payload, _ := json.Marshal([]domain.Matchup{matchup})
	if err := service.Store.SaveCommandSnapshot("match_scoreboard", source, service.League+":"+period.scope, "v1", string(payload)); err != nil {
		return err
	}
	for _, roster := range []domain.RosterWeekStats{mine, opponent} {
		payload, _ := json.Marshal(roster)
		if err := service.Store.SaveCommandSnapshot("match_roster", source, fmt.Sprintf("%s:%d", roster.TeamKey, matchup.Week), version, string(payload)); err != nil {
			return err
		}
	}
	return nil
}

func (service *MatchupService) enrichView(view domain.MatchupView, period matchupPeriod, prefetch *matchupPrefetch) (domain.MatchupView, error) {
	if period.daily {
		if err := service.overlayDaily(&view, period.day); err != nil {
			return domain.MatchupView{}, err
		}
	}
	if period.gameContext && prefetch != nil && prefetch.schedule != nil {
		if schedule := <-prefetch.schedule; schedule.err == nil {
			games := schedule.games
			var local []domain.StoredFantasyPlayer
			identities := map[int64]int64{}
			if players, readErr := service.matchupFantasyPlayers(); readErr == nil {
				local = players
				identities = fantasyIdentityMap(players)
			}
			applyRosterStatusLabels(view.Mine.Players, local)
			applyRosterStatusLabels(view.Opponent.Players, local)
			applyMatchupGameStatus(view.Mine.Players, games, identities)
			applyMatchupGameStatus(view.Opponent.Players, games, identities)
			view.Odds = service.matchupOdds(games, view, local, prefetch)
		}
	}
	return view, nil
}

// dailyStatsPayload is the stored daily MLB overlay for one date.
type dailyStatsPayload struct {
	Hitting  []providers.BulkHittingSplit  `json:"hitting"`
	Pitching []providers.BulkPitchingSplit `json:"pitching"`
}

// storedDailyStatsWithinWindow serves the day's overlay snapshot while it is
// inside the matchup reuse window.
func (service *MatchupService) storedDailyStatsWithinWindow(day string) ([]providers.BulkHittingSplit, []providers.BulkPitchingSplit, bool) {
	snapshot, err := service.Store.CommandSnapshot("match_day_stats", "mlb", day)
	if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" || snapshot.Stale || service.now().Sub(snapshot.LastSuccessfulAt) > liveReuseWindow {
		return nil, nil, false
	}
	var payload dailyStatsPayload
	if json.Unmarshal([]byte(snapshot.Payload), &payload) != nil {
		return nil, nil, false
	}
	return payload.Hitting, payload.Pitching, true
}

func (service *MatchupService) saveDailyStatsSnapshot(day string, hitters []providers.BulkHittingSplit, pitchers []providers.BulkPitchingSplit) {
	if payload, err := json.Marshal(dailyStatsPayload{Hitting: hitters, Pitching: pitchers}); err == nil {
		_ = service.Store.SaveCommandSnapshot("match_day_stats", "mlb", day, "v1", string(payload))
	}
}

func (service *MatchupService) overlayDaily(view *domain.MatchupView, day string) error {
	local, err := service.Store.FantasyPlayers(service.League)
	if err != nil {
		return err
	}
	identities := fantasyIdentityMap(local)
	for _, player := range append(append([]domain.PlayerWeekStats{}, view.Mine.Players...), view.Opponent.Players...) {
		if _, ok := identities[player.YahooPlayerID]; !ok {
			return fmt.Errorf("match: daily MLB overlay requires a reconciled MLB identity for %s; run skout sync and retry", player.Name)
		}
	}
	hitters, pitchers, reused := service.storedDailyStatsWithinWindow(day)
	if !reused {
		if service.HittingRange == nil || service.PitchingRange == nil {
			return fmt.Errorf("match: daily MLB statistics providers are unavailable")
		}
		var hitterErr, pitcherErr error
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			hitters, hitterErr = service.HittingRange(int64(service.Season), day, day)
		}()
		go func() {
			defer workers.Done()
			pitchers, pitcherErr = service.PitchingRange(int64(service.Season), day, day)
		}()
		workers.Wait()
		if hitterErr != nil || pitcherErr != nil {
			return fmt.Errorf("match: acquire complete daily MLB statistics: %v", errorsJoin(hitterErr, pitcherErr))
		}
		service.saveDailyStatsSnapshot(day, hitters, pitchers)
	}
	hitting := map[int64]providers.HittingStats{}
	for _, row := range hitters {
		hitting[row.Player.PersonID] = row.Stat
	}
	pitching := map[int64]providers.PitchingStats{}
	for _, row := range pitchers {
		pitching[row.Player.PersonID] = row.Stat
	}
	apply := func(players []domain.PlayerWeekStats) {
		for index := range players {
			player := &players[index]
			player.HAB, player.Runs, player.HomeRuns, player.RunsBattedIn, player.StolenBases, player.BattingAverage = "", 0, 0, 0, 0, ""
			player.InningsPitched, player.Wins, player.Saves, player.Strikeouts, player.EarnedRunAverage, player.WHIP = "", 0, 0, 0, "", ""
			id := identities[player.YahooPlayerID]
			if player.PositionType == "P" {
				if row, ok := pitching[id]; ok {
					player.InningsPitched, player.Wins, player.Saves, player.Strikeouts, player.EarnedRunAverage, player.WHIP = row.InningsPitched, int(row.Wins), int(row.Saves), int(row.Strikeouts), row.ERA, row.WHIP
				}
			} else if row, ok := hitting[id]; ok {
				player.HAB = fmt.Sprintf("%d/%d", row.Hits, row.AtBats)
				player.Runs, player.HomeRuns, player.RunsBattedIn, player.StolenBases, player.BattingAverage = int(row.Runs), int(row.HomeRuns), int(row.RBI), int(row.StolenBases), row.Average
			}
		}
	}
	apply(view.Mine.Players)
	apply(view.Opponent.Players)
	return nil
}

func (service *MatchupService) matchupOdds(games []providers.ScheduleGame, view domain.MatchupView, players []domain.StoredFantasyPlayer, prefetch *matchupPrefetch) []domain.MatchupOdds {
	if prefetch == nil || prefetch.odds == nil {
		return nil
	}
	odds := <-prefetch.odds
	if odds.err != nil {
		return nil
	}
	lines := odds.lines
	var output []domain.MatchupOdds
	for _, game := range games {
		var line *providers.ESPNGameLine
		for index := range lines.Games {
			if normalizedClub(lines.Games[index].HomeTeam) == normalizedClub(game.HomeTeamName) && normalizedClub(lines.Games[index].AwayTeam) == normalizedClub(game.AwayTeamName) && lines.Games[index].Quoted {
				line = &lines.Games[index]
				break
			}
		}
		if line == nil {
			continue
		}
		away, home := normalizedProbabilities(line.AwayMoneyline, line.HomeMoneyline)
		for _, candidate := range []struct {
			id             *int64
			name, opponent string
			probability    float64
		}{{game.AwayProbablePitcherID, game.AwayProbablePitcherName, game.HomeProbablePitcherName, away}, {game.HomeProbablePitcherID, game.HomeProbablePitcherName, game.AwayProbablePitcherName, home}} {
			if candidate.id == nil {
				continue
			}
			mine := rosterHasMLBIdentity(players, view.Mine.Players, *candidate.id)
			opponent := rosterHasMLBIdentity(players, view.Opponent.Players, *candidate.id)
			if !mine && !opponent {
				continue
			}
			percent := int(math.Round(candidate.probability * 100))
			line := fmt.Sprintf("%s v %s  %-7s  %s", oddsName(candidate.name), oddsName(candidate.opponent), MLBTeamAbbreviation(game.AwayTeamID)+"@"+MLBTeamAbbreviation(game.HomeTeamID), display.OddsBar(&percent, service.Mode))
			output = append(output, domain.MatchupOdds{Mine: mine, Line: line})
		}
	}
	return output
}

func (service *MatchupService) weekForDay(day string, current int, team string) (int, error) {
	for week := 1; week <= current; week++ {
		rows, _, err := service.scoreboard(week, week < current)
		if err != nil {
			return 0, fantasyError("match", "resolve day matchup", err)
		}
		for _, matchup := range rows {
			if matchupHasTeam(matchup, team) && matchup.WeekStart <= day && day <= matchup.WeekEnd {
				return week, nil
			}
		}
	}
	return 0, fmt.Errorf("match: day is outside the available Yahoo matchup weeks")
}

func (service *MatchupService) localFallback(selected store.StoredFantasyTeam) (domain.LocalMatchupView, error) {
	players, err := service.matchupFantasyPlayers()
	if err != nil {
		return domain.LocalMatchupView{}, err
	}
	var roster []domain.StoredFantasyPlayer
	for _, player := range players {
		if player.Owner != nil && *player.Owner == selected.Name {
			roster = append(roster, player)
		}
	}
	if len(roster) == 0 {
		return domain.LocalMatchupView{}, fmt.Errorf("no local roster is available; run skout sync and retry")
	}
	return domain.LocalMatchupView{TeamName: selected.Name, Players: roster}, nil
}

func (service *MatchupService) now() time.Time {
	if service.Now == nil {
		return time.Now()
	}
	return service.Now()
}

func matchFeed(matchups []domain.Matchup, rosters map[string]domain.RosterWeekStats, team string, week int) (domain.Matchup, domain.RosterWeekStats, domain.RosterWeekStats, error) {
	if !validMatchupRows(matchups) {
		return domain.Matchup{}, domain.RosterWeekStats{}, domain.RosterWeekStats{}, fmt.Errorf("public Yahoo scoreboard is incomplete")
	}
	for _, matchup := range matchups {
		if matchup.Week != week || !matchupHasTeam(matchup, team) {
			continue
		}
		opponent := matchup.Teams[0].TeamKey
		if opponent == team {
			opponent = matchup.Teams[1].TeamKey
		}
		mine, mineOK := rosters[team]
		theirs, theirsOK := rosters[opponent]
		if mineOK && theirsOK && validRoster(mine, team, week) && validRoster(theirs, opponent, week) {
			return matchup, mine, theirs, nil
		}
	}
	return domain.Matchup{}, domain.RosterWeekStats{}, domain.RosterWeekStats{}, fmt.Errorf("public Yahoo feed has no complete selected-team matchup")
}

func validMatchupRows(rows []domain.Matchup) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if row.Week <= 0 || !domain.IsValidISODate(row.WeekStart) || !domain.IsValidISODate(row.WeekEnd) || row.WeekStart > row.WeekEnd || row.Teams[0].TeamKey == "" || row.Teams[1].TeamKey == "" || row.Teams[0].TeamKey == row.Teams[1].TeamKey || row.Teams[0].Stats == nil || row.Teams[1].Stats == nil {
			return false
		}
	}
	return true
}

func validRoster(row domain.RosterWeekStats, team string, week int) bool {
	if row.TeamKey != team || row.Week != week || len(row.Players) == 0 {
		return false
	}
	for _, player := range row.Players {
		if player.YahooPlayerID <= 0 || strings.TrimSpace(player.Name) == "" || player.PositionType != "B" && player.PositionType != "P" {
			return false
		}
	}
	return true
}

func resolveSeasonDay(value string, season int) (string, error) {
	if len(value) != 6 || value[3] != '-' {
		return "", fmt.Errorf("match: day must use MMM-DD")
	}
	month := strings.ToUpper(value[:1]) + strings.ToLower(value[1:3])
	parsed, err := time.Parse("Jan-02", month+value[3:])
	if err != nil {
		return "", fmt.Errorf("match: day must use MMM-DD")
	}
	resolved := fmt.Sprintf("%04d-%02d-%02d", season, parsed.Month(), parsed.Day())
	if !domain.IsValidISODate(resolved) {
		return "", fmt.Errorf("match: day is not valid in the active season; correct the date and retry")
	}
	return resolved, nil
}

func fantasyTeamsFromStore(input []store.StoredFantasyTeam) []domain.FantasyTeam {
	output := make([]domain.FantasyTeam, 0, len(input))
	for _, team := range input {
		output = append(output, domain.FantasyTeam{TeamKey: team.TeamKey, TeamID: team.TeamID, Name: team.Name, ManagerName: team.ManagerName, WaiverPriority: team.WaiverPriority, FAABBalance: team.FAABBalance, Wins: team.Wins, Losses: team.Losses, Ties: team.Ties, Moves: team.Moves, Rank: team.Rank})
	}
	return output
}

func fantasyIdentityMap(players []domain.StoredFantasyPlayer) map[int64]int64 {
	identities := make(map[int64]int64)
	for _, player := range players {
		if player.YahooPlayerID != nil && player.MLBAMID != nil {
			identities[*player.YahooPlayerID] = *player.MLBAMID
		}
	}
	return identities
}

// applyRosterStatusLabels replaces an injured-list label from the matchup feed,
// usually a bare IL, with the local roster label for the same Yahoo player so
// the matchup and roster views agree on the injured-list length.
func applyRosterStatusLabels(players []domain.PlayerWeekStats, local []domain.StoredFantasyPlayer) {
	labels := make(map[int64]string, len(local))
	for _, player := range local {
		if player.YahooPlayerID != nil && strings.HasPrefix(strings.ToUpper(player.Status), "IL") {
			labels[*player.YahooPlayerID] = player.Status
		}
	}
	for index := range players {
		player := &players[index]
		if label, ok := labels[player.YahooPlayerID]; ok && strings.HasPrefix(strings.ToUpper(player.InjuryStatus), "IL") {
			player.InjuryStatus = label
		}
	}
}

// keepsRosterStatus reports whether a player's injured-list or not-active
// status stays visible instead of the day's game status.
func keepsRosterStatus(status string) bool {
	upper := strings.ToUpper(strings.TrimSpace(status))
	return upper == "NA" || strings.HasPrefix(upper, "IL")
}

func applyMatchupGameStatus(players []domain.PlayerWeekStats, games []providers.ScheduleGame, identities map[int64]int64) {
	for index := range players {
		if keepsRosterStatus(players[index].InjuryStatus) {
			continue
		}
		for _, game := range games {
			player := &players[index]
			away := MLBTeamAbbreviation(game.AwayTeamID) == player.Team
			home := MLBTeamAbbreviation(game.HomeTeamID) == player.Team
			if !away && !home {
				continue
			}
			lineup, probable := game.HomeLineup, game.HomeProbablePitcherID
			if away {
				lineup, probable = game.AwayLineup, game.AwayProbablePitcherID
			}
			player.GameIndicator = domain.GameIndicator{}
			if mlbamID, ok := identities[player.YahooPlayerID]; ok {
				if player.PositionType == "P" && probable != nil && *probable == mlbamID {
					player.GameIndicator.Kind = domain.GameIndicatorStartingPitcher
				} else if player.PositionType == "B" && len(lineup) > 0 {
					player.GameIndicator.Kind = domain.GameIndicatorOutOfLineup
					for order, entry := range lineup {
						if entry.PersonID == mlbamID {
							player.GameIndicator = domain.GameIndicator{Kind: domain.GameIndicatorBattingOrder, Order: order + 1}
							break
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
			location := "v " + MLBTeamAbbreviation(game.AwayTeamID)
			if away {
				location = "@ " + MLBTeamAbbreviation(game.HomeTeamID)
			}
			if strings.EqualFold(game.DetailedState, "Final") {
				player.InjuryStatus = "Final " + location
			} else if !gameNotStarted(game.DetailedState) && game.Linescore != nil {
				player.InjuryStatus = joinStatusParts(firstRune(game.Linescore.InningState)+game.Linescore.InningOrdinal, marker, location)
			} else {
				gameTime := game.GameDate
				if parsed, err := time.Parse(time.RFC3339, game.GameDate); err == nil {
					gameTime = parsed.Local().Format("3:04p")
				}
				if gameTime == "" {
					gameTime = "Scheduled"
				}
				player.InjuryStatus = joinStatusParts(gameTime, marker, location)
			}
			break
		}
	}
}

// joinStatusParts joins the non-empty status fragments with single spaces.
func joinStatusParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " ")
}

func rosterHasMLBIdentity(players []domain.StoredFantasyPlayer, roster []domain.PlayerWeekStats, id int64) bool {
	yahoo := map[int64]struct{}{}
	for _, player := range roster {
		yahoo[player.YahooPlayerID] = struct{}{}
	}
	for _, player := range players {
		if player.YahooPlayerID != nil && player.MLBAMID != nil && *player.MLBAMID == id {
			_, exists := yahoo[*player.YahooPlayerID]
			return exists
		}
	}
	return false
}

func normalizedProbabilities(awayLine, homeLine int64) (float64, float64) {
	implied := func(line int64) float64 {
		if line > 0 {
			return 100 / float64(line+100)
		}
		if line < 0 {
			return float64(-line) / float64(-line+100)
		}
		return 0
	}
	away, home := implied(awayLine), implied(homeLine)
	if away+home == 0 {
		return 0, 0
	}
	return away / (away + home), home / (away + home)
}

func normalizedClub(value string) string {
	var output strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func lastName(value string) string {
	value, _, _ = strings.Cut(value, " (")
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func oddsName(value string) string {
	runes := []rune(lastName(value))
	if len(runes) > 16 {
		runes = runes[:16]
	}
	return string(runes) + strings.Repeat(" ", 16-len(runes))
}

func errorsJoin(left, right error) error {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return fmt.Errorf("%v; %v", left, right)
}

// StableMatchupPlayers orders a roster by slot then Yahoo identity.
func StableMatchupPlayers(players []domain.PlayerWeekStats) {
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].SlotPosition != players[j].SlotPosition {
			return players[i].SlotPosition < players[j].SlotPosition
		}
		return players[i].YahooPlayerID < players[j].YahooPlayerID
	})
}
