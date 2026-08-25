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
	service.Now = func() time.Time { return now.Add(60 * time.Second) }
	if _, err := service.Matchup(MatchupOptions{}); err != nil || calls != 0 {
		t.Fatalf("boundary calls=%d err=%v", calls, err)
	}
	service.Now = func() time.Time { return now.Add(61 * time.Second) }
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
	if err != nil || !strings.Contains(output, "YAHOO UNAVAILABLE") || strings.Contains(output, "SUMMARY") || !strings.Contains(output, "Operators") {
		t.Fatalf("local output=%q err=%v", output, err)
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
