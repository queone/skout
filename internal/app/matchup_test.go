package app

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/terminal"
)

func matchupFeed() providers.RedzoneFeed {
	matchup := domain.Matchup{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{{TeamKey: "mlb.l.1.t.1", TeamID: 1, Name: "Operators", Stats: map[string]string{"7": "5"}, Wins: 1}, {TeamKey: "mlb.l.1.t.2", TeamID: 2, Name: "Rivals", Stats: map[string]string{"7": "3"}, Losses: 1}}}
	return providers.RedzoneFeed{
		Teams:    []domain.FantasyTeam{{TeamKey: "mlb.l.1.t.1", TeamID: 1, Name: "Operators", Rank: 1}, {TeamKey: "mlb.l.1.t.2", TeamID: 2, Name: "Rivals", Rank: 2}},
		Matchups: []domain.Matchup{matchup}, Week: 7,
		RosterWeekStats: map[string]domain.RosterWeekStats{
			"mlb.l.1.t.1": {TeamKey: "mlb.l.1.t.1", TeamName: "Operators", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 101, Name: "Ada Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionOutfield}, {YahooPlayerID: 102, Name: "Ace Pitcher", Team: "BOS", PositionType: "P", SlotPosition: domain.PositionStartingPitcher}}},
			"mlb.l.1.t.2": {TeamKey: "mlb.l.1.t.2", TeamName: "Rivals", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 201, Name: "Rival Hitter", Team: "TB", PositionType: "B", SlotPosition: domain.PositionOutfield}, {YahooPlayerID: 202, Name: "Rival Pitcher", Team: "TOR", PositionType: "P", SlotPosition: domain.PositionStartingPitcher}}},
		},
	}
}

func matchupServiceForTest(t *testing.T, now time.Time) *MatchupService {
	database := fantasyAppStore(t, now)
	feed := matchupFeed()
	return &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026, Now: func() time.Time { return now }, Mode: terminal.Plain,
		FetchRedzone: func(string, string) (providers.RedzoneFeed, error) { return feed, nil },
		Scoreboard:   func(string, *int) ([]domain.Matchup, error) { return feed.Matchups, nil },
		RosterWeek:   func(team string, _ int) (domain.RosterWeekStats, error) { return feed.RosterWeekStats[team], nil },
		RosterDay: func(team string, _ int, _ string) (domain.RosterWeekStats, error) {
			return feed.RosterWeekStats[team], nil
		},
		HittingRange: func(int64, string, string) ([]providers.BulkHittingSplit, error) {
			return []providers.BulkHittingSplit{{Player: providers.BulkPlayer{PersonID: 1001}, Stat: providers.HittingStats{Hits: 2, AtBats: 4, Runs: 1, Average: ".500"}}, {Player: providers.BulkPlayer{PersonID: 2001}, Stat: providers.HittingStats{Hits: 1, AtBats: 4, Average: ".250"}}}, nil
		},
		PitchingRange: func(int64, string, string) ([]providers.BulkPitchingSplit, error) {
			return []providers.BulkPitchingSplit{{Player: providers.BulkPlayer{PersonID: 1002}, Stat: providers.PitchingStats{InningsPitched: "6.0", Wins: 1, Strikeouts: 7, ERA: "3.00", WHIP: "1.00"}}, {Player: providers.BulkPlayer{PersonID: 2002}, Stat: providers.PitchingStats{InningsPitched: "5.0", Strikeouts: 5, ERA: "4.50", WHIP: "1.20"}}}, nil
		},
		Schedule: func(string) ([]providers.ScheduleGame, error) { return nil, nil },
	}
}

func TestMatchupDefaultFetchesRedzoneOncePersistsExactSnapshotsAndOverlays(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	calls := 0
	original := service.FetchRedzone
	service.FetchRedzone = func(id, league string) (providers.RedzoneFeed, error) { calls++; return original(id, league) }
	output, err := service.Matchup(MatchupOptions{})
	if err != nil || calls != 1 || !strings.Contains(output, "MATCHUP WEEK:") || !strings.Contains(output, "2/4") || !strings.Contains(output, "6.0") {
		t.Fatalf("calls=%d output=%q err=%v", calls, output, err)
	}
	for _, target := range []struct{ dataset, source, scope, version string }{{"match_scoreboard", "yahoo_public", "mlb.l.1:current", "v1"}, {"match_roster", "yahoo_public", "mlb.l.1.t.1:7", "v1"}, {"match_roster", "yahoo_public", "mlb.l.1.t.2:7", "v1"}} {
		snapshot, readErr := service.Store.CommandSnapshot(target.dataset, target.source, target.scope)
		if readErr != nil || snapshot == nil || snapshot.SnapshotVersion != target.version {
			t.Fatalf("target=%#v snapshot=%#v err=%v", target, snapshot, readErr)
		}
	}
}

func TestMatchupPrefetchesScheduleAndOddsBeforeYahooCompletes(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	scheduleStarted := make(chan struct{})
	oddsStarted := make(chan struct{})
	service.Schedule = func(string) ([]providers.ScheduleGame, error) { close(scheduleStarted); return nil, nil }
	service.Odds = func(time.Time) (providers.ESPNSlateLines, error) {
		close(oddsStarted)
		return providers.ESPNSlateLines{}, nil
	}
	original := service.FetchRedzone
	service.FetchRedzone = func(id, league string) (providers.RedzoneFeed, error) {
		<-scheduleStarted
		<-oddsStarted
		return original(id, league)
	}
	if _, err := service.Matchup(MatchupOptions{}); err != nil {
		t.Fatalf("overlapped matchup err=%v", err)
	}
}

func TestScheduledGamesWithLinescoreStubsShowGameTimesNotEmptyStatus(t *testing.T) {
	players := []domain.PlayerWeekStats{
		{YahooPlayerID: 101, Name: "Ada Hitter", Team: "NYY", PositionType: "B"},
		{YahooPlayerID: 102, Name: "Live Hitter", Team: "BOS", PositionType: "B"},
	}
	games := []providers.ScheduleGame{
		{GameID: 1, AwayTeamID: 147, HomeTeamID: 139, DetailedState: "Scheduled", GameDate: "2026-09-01T23:40:00Z", Linescore: &providers.Linescore{}},
		{GameID: 2, AwayTeamID: 111, HomeTeamID: 121, DetailedState: "In Progress", GameDate: "2026-09-01T17:10:00Z", Linescore: &providers.Linescore{InningState: "Top", InningOrdinal: "3rd"}},
	}
	applyMatchupGameStatus(players, games, map[int64]int64{})
	if players[0].InjuryStatus == "" || strings.Contains(players[0].InjuryStatus, "3rd") || !strings.HasSuffix(players[0].InjuryStatus, "@ TB") {
		t.Fatalf("scheduled status=%q", players[0].InjuryStatus)
	}
	if players[1].InjuryStatus != "T3rd @ NYM" {
		t.Fatalf("live status=%q", players[1].InjuryStatus)
	}
}

func TestMatchupGameStatusKeepsInjuredAndInactiveLabels(t *testing.T) {
	players := []domain.PlayerWeekStats{
		{YahooPlayerID: 101, Name: "Injured Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionInjuredList, InjuryStatus: "IL15"},
		{YahooPlayerID: 102, Name: "Inactive Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionBench, InjuryStatus: "NA"},
		{YahooPlayerID: 103, Name: "Active Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionOutfield},
	}
	games := []providers.ScheduleGame{{GameID: 1, AwayTeamID: 147, HomeTeamID: 139, DetailedState: "Scheduled", GameDate: "2026-09-01T23:40:00Z", AwayLineup: []providers.LineupPlayer{{PersonID: 1001}, {PersonID: 1003}}}}
	applyMatchupGameStatus(players, games, map[int64]int64{101: 1001, 102: 1002, 103: 1003})
	if players[0].InjuryStatus != "IL15" || players[0].GameIndicator != (domain.GameIndicator{}) {
		t.Fatalf("injured=%+v", players[0])
	}
	if players[1].InjuryStatus != "NA" || players[1].GameIndicator != (domain.GameIndicator{}) {
		t.Fatalf("inactive=%+v", players[1])
	}
	if !strings.HasSuffix(players[2].InjuryStatus, "2 @ TB") || players[2].GameIndicator.Kind != domain.GameIndicatorBattingOrder {
		t.Fatalf("active=%+v", players[2])
	}
}

func TestMatchupRosterStatusLabelsReplaceBareInjuredListLabels(t *testing.T) {
	ids := []int64{101, 102, 103}
	local := []domain.StoredFantasyPlayer{{YahooPlayerID: &ids[0], Status: "IL15"}, {YahooPlayerID: &ids[1], Status: "IL60"}, {YahooPlayerID: &ids[2], Status: "IL10"}}
	players := []domain.PlayerWeekStats{
		{YahooPlayerID: 101, InjuryStatus: "IL"},
		{YahooPlayerID: 102, InjuryStatus: "7:05p v BOS"},
		{YahooPlayerID: 103, InjuryStatus: "NA"},
		{YahooPlayerID: 104, InjuryStatus: "IL"},
	}
	applyRosterStatusLabels(players, local)
	if players[0].InjuryStatus != "IL15" || players[1].InjuryStatus != "7:05p v BOS" || players[2].InjuryStatus != "NA" || players[3].InjuryStatus != "IL" {
		t.Fatalf("players=%+v", players)
	}
}

func TestMatchupEnforcesTheSixHourAutoSyncGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	calls := 0
	service.AutoSync = func() error {
		calls++
		return service.Store.MarkSyncItemSuccess("yahoo_public", "fantasy", "mlb.l.1", syncPipelineVersion)
	}
	if _, err := service.Matchup(MatchupOptions{}); err != nil || calls != 1 {
		t.Fatalf("missing sync state: calls=%d err=%v", calls, err)
	}
	if _, err := service.Matchup(MatchupOptions{}); err != nil || calls != 1 {
		t.Fatalf("fresh sync state: calls=%d err=%v", calls, err)
	}
}

func TestArchivedSeasonMatchupServesStoredWeeklyPayloadsWithoutProviders(t *testing.T) {
	now := time.Date(2027, 5, 1, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	feed := matchupFeed()
	scoreboard, err := json.Marshal(feed.Matchups)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7", "v1", string(scoreboard)); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"mlb.l.1.t.1", "mlb.l.1.t.2"} {
		payload, rosterErr := json.Marshal(feed.RosterWeekStats[team])
		if rosterErr != nil {
			t.Fatal(rosterErr)
		}
		if err := database.SaveCommandSnapshot("match_roster", "yahoo", team+":7", "v2", string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	service := &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026, ArchivedSeason: 2026,
		PersistTeam: func(string) error { return nil },
		Now:         func() time.Time { return now }, Mode: terminal.Plain,
	}
	defer service.Close()
	output, err := service.Matchup(MatchupOptions{})
	if err != nil || !strings.Contains(output, "ARCHIVED — season 2026") || !strings.Contains(output, "MATCHUP WEEK:") || strings.Contains(output, "·") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, err := service.Matchup(MatchupOptions{Day: "2026-08-25"}); err == nil || !strings.Contains(err.Error(), "archived season") {
		t.Fatalf("daily error=%v", err)
	}
	week, err := service.Matchup(MatchupOptions{Week: 7})
	if err != nil || !strings.Contains(week, "ARCHIVED — season 2026") {
		t.Fatalf("week output=%q err=%v", week, err)
	}
}

func TestMatchupValidationPrecedesProvidersAndNamedTeamPersistsOnlyOnSuccess(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	providerCalls, persisted := 0, ""
	service.FetchRedzone = func(string, string) (providers.RedzoneFeed, error) { providerCalls++; return matchupFeed(), nil }
	service.Scoreboard = func(string, *int) ([]domain.Matchup, error) { providerCalls++; return matchupFeed().Matchups, nil }
	service.PersistTeam = func(team string) error { persisted = team; return nil }
	if _, err := service.Matchup(MatchupOptions{Week: 8}); err == nil || providerCalls != 0 {
		t.Fatalf("future error=%v provider calls=%d", err, providerCalls)
	}
	if _, err := service.Matchup(MatchupOptions{Team: "nobody"}); err == nil || persisted != "" || providerCalls != 0 {
		t.Fatalf("missing error=%v persisted=%q calls=%d", err, persisted, providerCalls)
	}
	if _, err := service.Matchup(MatchupOptions{Team: "Grace"}); err != nil || persisted != "mlb.l.1.t.2" || providerCalls != 1 {
		t.Fatalf("selected error=%v persisted=%q calls=%d", err, persisted, providerCalls)
	}
}

func TestMatchupLeagueOverridePreventsTeamPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := productionMatchupTeamPersister(true)("mlb.l.1.t.2"); err != nil {
		t.Fatal(err)
	}
	settings, err := config.Read()
	if err != nil || settings.CurrentTeamKey != "" {
		t.Fatalf("override settings=%#v err=%v", settings, err)
	}
	if err := productionMatchupTeamPersister(false)("mlb.l.1.t.2"); err != nil {
		t.Fatal(err)
	}
	settings, err = config.Read()
	if err != nil || settings.CurrentTeamKey != "mlb.l.1.t.2" {
		t.Fatalf("saved settings=%#v err=%v", settings, err)
	}
}

func TestMatchupFreshnessBoundaryAndLocalFallback(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	if _, err := service.Matchup(MatchupOptions{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.FetchRedzone = func(string, string) (providers.RedzoneFeed, error) {
		calls++
		return providers.RedzoneFeed{}, errors.New("offline")
	}
	service.Now = func() time.Time { return now.Add(liveReuseWindow) }
	if _, err := service.Matchup(MatchupOptions{}); err != nil || calls != 0 {
		t.Fatalf("boundary calls=%d err=%v", calls, err)
	}
	service.Now = func() time.Time { return now.Add(liveReuseWindow + time.Second) }
	output, err := service.Matchup(MatchupOptions{})
	if err != nil || calls != 1 || !strings.Contains(output, "STALE") {
		t.Fatalf("stale calls=%d output=%q err=%v", calls, output, err)
	}

	local := matchupServiceForTest(t, now)
	defer local.Close()
	local.FetchRedzone = func(string, string) (providers.RedzoneFeed, error) {
		return providers.RedzoneFeed{}, errors.New("offline")
	}
	output, err = local.Matchup(MatchupOptions{})
	if err != nil || !strings.Contains(output, "YAHOO UNAVAILABLE") || strings.Contains(output, "SCORE") || !strings.Contains(output, "Operators") {
		t.Fatalf("local output=%q err=%v", output, err)
	}
}

func TestMatchupReusesViewOddsAndDailyOverlayWithinTheWindow(t *testing.T) {
	current := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStoreWithClock(t, adjustableClock{&current})
	feed := matchupFeed()
	redzone, odds, hitting, pitching := 0, 0, 0, 0
	service := &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026,
		Now: func() time.Time { return current }, Mode: terminal.Plain,
		FetchRedzone: func(string, string) (providers.RedzoneFeed, error) { redzone++; return feed, nil },
		HittingRange: func(int64, string, string) ([]providers.BulkHittingSplit, error) { hitting++; return nil, nil },
		PitchingRange: func(int64, string, string) ([]providers.BulkPitchingSplit, error) {
			pitching++
			return nil, nil
		},
		Schedule: func(string) ([]providers.ScheduleGame, error) { return nil, nil },
		Odds:     func(time.Time) (providers.ESPNSlateLines, error) { odds++; return providers.ESPNSlateLines{}, nil },
	}
	defer service.Close()
	if _, err := service.Matchup(MatchupOptions{}); err != nil || redzone != 1 || odds != 1 || hitting != 1 || pitching != 1 {
		t.Fatalf("first render redzone=%d odds=%d hitting=%d pitching=%d err=%v", redzone, odds, hitting, pitching, err)
	}
	if _, err := service.Matchup(MatchupOptions{}); err != nil || redzone != 1 || odds != 1 || hitting != 1 || pitching != 1 {
		t.Fatalf("reused render redzone=%d odds=%d hitting=%d pitching=%d err=%v", redzone, odds, hitting, pitching, err)
	}
	current = current.Add(liveReuseWindow + time.Minute)
	if _, err := service.Matchup(MatchupOptions{}); err != nil || redzone != 2 || odds != 2 || hitting != 2 || pitching != 2 {
		t.Fatalf("lapsed render redzone=%d odds=%d hitting=%d pitching=%d err=%v", redzone, odds, hitting, pitching, err)
	}
}

func TestDailyOverlayStartsBothProviderReadsBeforeApplyingEither(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	feed := matchupFeed()
	view := domain.MatchupView{Matchup: feed.Matchups[0], Mine: feed.RosterWeekStats["mlb.l.1.t.1"], Opponent: feed.RosterWeekStats["mlb.l.1.t.2"]}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	service.HittingRange = func(int64, string, string) ([]providers.BulkHittingSplit, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}
	service.PitchingRange = func(int64, string, string) ([]providers.BulkPitchingSplit, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}
	done := make(chan error, 1)
	go func() { done <- service.overlayDaily(&view, "2026-08-25") }()
	<-started
	<-started
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalMatchupPrefersCompleteSnapshotsWithoutProviders(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	feed := matchupFeed()
	matchup := feed.Matchups[0]
	matchup.Week, matchup.WeekStart, matchup.WeekEnd = 6, "2026-08-17", "2026-08-23"
	mine, opponent := feed.RosterWeekStats["mlb.l.1.t.1"], feed.RosterWeekStats["mlb.l.1.t.2"]
	mine.Week, opponent.Week = 6, 6
	for _, target := range []struct {
		dataset, scope, version string
		value                   any
	}{{"match_scoreboard", "mlb.l.1:6", "v1", []domain.Matchup{matchup}}, {"match_roster", "mlb.l.1.t.1:6", "v2", mine}, {"match_roster", "mlb.l.1.t.2:6", "v2", opponent}} {
		payload, _ := json.Marshal(target.value)
		if err := service.Store.SaveCommandSnapshot(target.dataset, "yahoo", target.scope, target.version, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	providerCalls := 0
	service.Scoreboard = func(string, *int) ([]domain.Matchup, error) { providerCalls++; return nil, errors.New("unexpected") }
	service.RosterWeek = func(string, int) (domain.RosterWeekStats, error) {
		providerCalls++
		return domain.RosterWeekStats{}, errors.New("unexpected")
	}
	output, err := service.Matchup(MatchupOptions{Week: 6})
	if err != nil || providerCalls != 0 || !strings.Contains(output, "MATCHUP WEEK: 6") {
		t.Fatalf("calls=%d output=%q err=%v", providerCalls, output, err)
	}
}

func TestMatchupDoesNotPersistSelectionOrUseYahooFallbackForMLBFailure(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	persisted := ""
	service.PersistTeam = func(team string) error { persisted = team; return nil }
	service.HittingRange = func(int64, string, string) ([]providers.BulkHittingSplit, error) {
		return nil, errors.New("mlb offline")
	}
	output, err := service.Matchup(MatchupOptions{Team: "Grace"})
	if err == nil || output != "" || persisted != "" || strings.Contains(output, "YAHOO UNAVAILABLE") {
		t.Fatalf("output=%q persisted=%q err=%v", output, persisted, err)
	}
}

func TestMatchupGameContextAppliesUniqueLineupAndProbableMarkers(t *testing.T) {
	pitcher := int64(1002)
	players := []domain.PlayerWeekStats{{YahooPlayerID: 101, Name: "Ada Hitter", Team: "NYY", PositionType: "B"}, {YahooPlayerID: 102, Name: "Ace Pitcher", Team: "NYY", PositionType: "P"}}
	games := []providers.ScheduleGame{{AwayTeamID: 147, HomeTeamID: 111, DetailedState: "Scheduled", AwayLineup: []providers.LineupPlayer{{PersonID: 1001}}, AwayProbablePitcherID: &pitcher}}
	applyMatchupGameStatus(players, games, map[int64]int64{101: 1001, 102: 1002})
	if players[0].GameIndicator.Kind != domain.GameIndicatorBattingOrder || players[0].GameIndicator.Order != 1 || !strings.Contains(players[0].InjuryStatus, "1") || players[1].GameIndicator.Kind != domain.GameIndicatorStartingPitcher || !strings.Contains(players[1].InjuryStatus, "●") {
		t.Fatalf("players=%#v", players)
	}
}

func TestDailyYahooFixtureNormalizesSelectedDateRoster(t *testing.T) {
	payload, err := os.ReadFile("../providers/testdata/yahoo/daily-stats.json")
	if err != nil {
		t.Fatal(err)
	}
	roster, err := providers.ParseRosterWeekStats("mlb.l.1.t.1", 7, payload)
	if err != nil || len(roster.Players) != 1 || roster.Players[0].HAB != "2/4" || roster.Players[0].HomeRuns != 1 {
		t.Fatalf("roster=%#v err=%v", roster, err)
	}
}

func leagueMatchupRows() []domain.Matchup {
	return []domain.Matchup{
		{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{{TeamKey: "mlb.l.1.t.3", TeamID: 3, Name: "Sluggers", Stats: map[string]string{"7": "30"}, Wins: 5, Losses: 5}, {TeamKey: "mlb.l.1.t.4", TeamID: 4, Name: "Closers", Stats: map[string]string{"7": "27"}, Wins: 5, Losses: 5}}},
		{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{{TeamKey: "mlb.l.1.t.1", TeamID: 1, Name: "Operators", Stats: map[string]string{"7": "29"}, Wins: 7, Ties: 2, Losses: 1}, {TeamKey: "mlb.l.1.t.2", TeamID: 2, Name: "Rivals", Stats: map[string]string{"7": "19"}, Wins: 1, Ties: 2, Losses: 7}}},
	}
}

func TestLeagueMatchupsFetchesScoreboardOncePersistsAndReusesWithinWindow(t *testing.T) {
	current := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStoreWithClock(t, adjustableClock{&current})
	calls := 0
	service := &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026,
		Now: func() time.Time { return current }, Mode: terminal.Plain,
		Scoreboard: func(string, *int) ([]domain.Matchup, error) { calls++; return leagueMatchupRows(), nil },
	}
	defer service.Close()
	output, err := service.LeagueMatchups(LeagueMatchupsOptions{})
	if err != nil || calls != 1 || !strings.Contains(output, "MATCHUPS WEEK: 7 of 26") {
		t.Fatalf("calls=%d output=%q err=%v", calls, output, err)
	}
	for _, name := range []string{"Operators", "Rivals", "Sluggers", "Closers"} {
		if !strings.Contains(output, name) {
			t.Fatalf("missing %s in %q", name, output)
		}
	}
	if strings.Index(output, "Operators") > strings.Index(output, "Sluggers") {
		t.Fatalf("saved team's matchup is not first: %q", output)
	}
	snapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7")
	if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" || snapshot.Stale {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{}); err != nil || calls != 1 {
		t.Fatalf("reused calls=%d err=%v", calls, err)
	}
	current = current.Add(liveReuseWindow + time.Minute)
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{}); err != nil || calls != 2 {
		t.Fatalf("lapsed calls=%d err=%v", calls, err)
	}
}

func TestLeagueMatchupsServesStaleSnapshotAndNamesSyncWhenNoneExists(t *testing.T) {
	current := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStoreWithClock(t, adjustableClock{&current})
	payload, err := json.Marshal(leagueMatchupRows())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7", "v1", string(payload)); err != nil {
		t.Fatal(err)
	}
	current = current.Add(liveReuseWindow + time.Minute)
	failing := func(string, *int) ([]domain.Matchup, error) { return nil, errors.New("yahoo down") }
	service := &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026,
		Now: func() time.Time { return current }, Mode: terminal.Plain, Scoreboard: failing,
	}
	defer service.Close()
	output, err := service.LeagueMatchups(LeagueMatchupsOptions{})
	if err != nil || !strings.Contains(output, "STALE — Yahoo unavailable") || !strings.Contains(output, "Operators") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	snapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7")
	if err != nil || snapshot == nil || !snapshot.Stale {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	empty := &MatchupService{
		Store: fantasyAppStore(t, current), League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026,
		Now: func() time.Time { return current }, Mode: terminal.Plain, Scoreboard: failing,
	}
	defer empty.Close()
	if _, err := empty.LeagueMatchups(LeagueMatchupsOptions{}); err == nil || !strings.HasPrefix(err.Error(), "matchups:") || !strings.Contains(err.Error(), "skout sync") {
		t.Fatalf("err=%v", err)
	}
}

func TestLeagueMatchupsRejectsBadWeeksAndServesArchivedSeasonWithoutProviders(t *testing.T) {
	now := time.Date(2027, 5, 1, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	payload, err := json.Marshal(leagueMatchupRows())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7", "v1", string(payload)); err != nil {
		t.Fatal(err)
	}
	service := &MatchupService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026, ArchivedSeason: 2026,
		Now: func() time.Time { return now }, Mode: terminal.Plain,
	}
	defer service.Close()
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{Week: 8}); err == nil || err.Error() != "matchups: week 8 is in the future; choose week 7 or earlier" {
		t.Fatalf("future err=%v", err)
	}
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{Week: -1}); err == nil || err.Error() != "matchups: week must be positive" {
		t.Fatalf("negative err=%v", err)
	}
	output, err := service.LeagueMatchups(LeagueMatchupsOptions{})
	if err != nil || !strings.Contains(output, "ARCHIVED — season 2026") || !strings.Contains(output, "MATCHUPS WEEK: 7 of 26") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if week, err := service.LeagueMatchups(LeagueMatchupsOptions{Week: 7}); err != nil || week != output {
		t.Fatalf("week output=%q err=%v", week, err)
	}
}

func TestLeagueMatchupsEnforcesTheSixHourAutoSyncGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := &MatchupService{
		Store: fantasyAppStore(t, now), League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Season: 2026,
		Now: func() time.Time { return now }, Mode: terminal.Plain,
		Scoreboard: func(string, *int) ([]domain.Matchup, error) { return leagueMatchupRows(), nil },
	}
	defer service.Close()
	calls := 0
	service.AutoSync = func() error {
		calls++
		return service.Store.MarkSyncItemSuccess("yahoo_public", "fantasy", "mlb.l.1", syncPipelineVersion)
	}
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{}); err != nil || calls != 1 {
		t.Fatalf("missing sync state: calls=%d err=%v", calls, err)
	}
	if _, err := service.LeagueMatchups(LeagueMatchupsOptions{}); err != nil || calls != 1 {
		t.Fatalf("fresh sync state: calls=%d err=%v", calls, err)
	}
}

func TestMatchupDayLabelFollowsTheDisplayedPeriod(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	service := matchupServiceForTest(t, now)
	defer service.Close()
	daily, err := service.Matchup(MatchupOptions{})
	if err != nil || !strings.HasSuffix(strings.SplitN(daily, "\n", 2)[0], "  ·  Tue aug-25") {
		t.Fatalf("daily=%q err=%v", daily, err)
	}
	feed := matchupFeed()
	service.Scoreboard = func(_ string, week *int) ([]domain.Matchup, error) {
		rows := make([]domain.Matchup, len(feed.Matchups))
		copy(rows, feed.Matchups)
		if week != nil && *week != 7 {
			start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -7*(7-*week))
			for index := range rows {
				rows[index].Week, rows[index].WeekStart, rows[index].WeekEnd = *week, start.Format("2006-01-02"), start.AddDate(0, 0, 6).Format("2006-01-02")
			}
		}
		return rows, nil
	}
	selected, err := service.Matchup(MatchupOptions{Day: "Aug-24"})
	if err != nil || !strings.HasSuffix(strings.SplitN(selected, "\n", 2)[0], "  ·  Mon aug-24") {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
	for name, options := range map[string]MatchupOptions{"weekly": {Weekly: true}, "week": {Week: 7}} {
		output, err := service.Matchup(options)
		if err != nil || strings.Contains(output, "·") {
			t.Fatalf("%s output=%q err=%v", name, output, err)
		}
	}
}
