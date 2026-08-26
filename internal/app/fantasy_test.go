package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/analysis"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

type fantasyAppClock struct{ value time.Time }

func (clock fantasyAppClock) Now() time.Time { return clock.value }

func fantasyAppStore(t *testing.T, now time.Time) *store.Store {
	t.Helper()
	database, err := store.OpenAtWithClock(filepath.Join(t.TempDir(), "app.db"), fantasyAppClock{now})
	if err != nil {
		t.Fatal(err)
	}
	week := 7
	ranks := []int64{1, 2, 3, 4, 5}
	snapshot := store.FantasySnapshotWrite{
		League: domain.League{LeagueKey: "mlb.l.1", Name: "League", Season: 2026, NumTeams: 2, ScoringType: domain.ScoringHeadToHead}, CurrentWeek: &week,
		Categories: []store.CategoryWrite{{StatID: 7, Abbreviation: "R", Name: "Runs", Sequence: 1}}, Positions: []store.PositionWrite{{Position: "OF", Count: 1}},
		Teams: []domain.FantasyTeam{{TeamKey: "mlb.l.1.t.1", LeagueKey: "mlb.l.1", TeamID: 1, Name: "Operators", ManagerName: "Ada", Rank: 1, Wins: 4, Losses: 2}, {TeamKey: "mlb.l.1.t.2", LeagueKey: "mlb.l.1", TeamID: 2, Name: "Rivals", ManagerName: "Grace", Rank: 2, Wins: 2, Losses: 4}},
		Players: []domain.FantasyPlayer{
			{YahooPlayerID: 101, Name: "Ada Hitter", MLBTeam: "NYY", PositionType: "B", DisplayPosition: "OF", EligiblePositions: []domain.Position{domain.PositionOutfield}, YahooRank: &ranks[0]},
			{YahooPlayerID: 102, Name: "Ace Pitcher", MLBTeam: "BOS", PositionType: "P", DisplayPosition: "SP", EligiblePositions: []domain.Position{domain.PositionStartingPitcher}, YahooRank: &ranks[1]},
			{YahooPlayerID: 201, Name: "Rival Hitter", MLBTeam: "TB", PositionType: "B", DisplayPosition: "OF", EligiblePositions: []domain.Position{domain.PositionOutfield}, YahooRank: &ranks[2]},
			{YahooPlayerID: 202, Name: "Rival Pitcher", MLBTeam: "TOR", PositionType: "P", DisplayPosition: "SP", EligiblePositions: []domain.Position{domain.PositionStartingPitcher}, YahooRank: &ranks[3]},
			{YahooPlayerID: 301, Name: "Free Hitter", MLBTeam: "LAD", PositionType: "B", DisplayPosition: "OF", EligiblePositions: []domain.Position{domain.PositionOutfield}, YahooRank: &ranks[4]},
		},
		Slots: []domain.FantasyRosterSlot{{TeamKey: "mlb.l.1.t.1", YahooPlayerID: 101, SlotPosition: domain.PositionOutfield}, {TeamKey: "mlb.l.1.t.1", YahooPlayerID: 102, SlotPosition: domain.PositionStartingPitcher}, {TeamKey: "mlb.l.1.t.2", YahooPlayerID: 201, SlotPosition: domain.PositionOutfield}, {TeamKey: "mlb.l.1.t.2", YahooPlayerID: 202, SlotPosition: domain.PositionStartingPitcher}},
	}
	if err := database.ReplaceFantasySnapshot(snapshot); err != nil {
		database.Close()
		t.Fatal(err)
	}
	_, err = database.ReconcileMLBIdentities([]store.IdentityCandidate{{MLBAMID: 1001, Name: "Ada Hitter", Team: "NYY", Role: "B"}, {MLBAMID: 1002, Name: "Ace Pitcher", Team: "BOS", Role: "P"}, {MLBAMID: 2001, Name: "Rival Hitter", Team: "TB", Role: "B"}, {MLBAMID: 2002, Name: "Rival Pitcher", Team: "TOR", Role: "P"}, {MLBAMID: 3001, Name: "Free Hitter", Team: "LAD", Role: "B"}})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func TestFantasyServiceRosterTotalsPoolAndEffectiveLeague(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	defer database.Close()
	service := &FantasyService{Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Now: func() time.Time { return now }, Mode: terminal.Plain}
	roster, err := service.Roster("")
	if err != nil || !strings.Contains(roster, "ROSTER: Operators") || strings.Contains(strings.Split(roster, "\n")[1], "OWNER") {
		t.Fatalf("roster=%q err=%v", roster, err)
	}
	queried, err := service.Roster("grace")
	if err != nil || !strings.Contains(queried, "Rivals") || service.TeamKey != "mlb.l.1.t.1" {
		t.Fatalf("queried=%q team=%q err=%v", queried, service.TeamKey, err)
	}
	totals, err := service.Totals("")
	if err != nil || !strings.Contains(totals, "Operators") || !strings.Contains(totals, "Rivals") {
		t.Fatalf("totals=%q err=%v", totals, err)
	}
	pool, err := service.Pool("B", PlayerPoolOptions{Argument: "1", Position: "of"})
	if err != nil || strings.Count(pool, "\n") < 2 || !strings.Contains(pool, "Ada Hitter") || strings.Contains(pool, "Ace Pitcher") {
		t.Fatalf("pool=%q err=%v", pool, err)
	}
}

func TestFantasyDetailSnapshotsAndUsesOnlyItsStaleFallback(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	defer database.Close()
	service := &FantasyService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Now: func() time.Time { return now }, Mode: terminal.Plain,
		HitterGameLog: func(id, season int64) ([]providers.HittingGameLogEntry, error) {
			return []providers.HittingGameLogEntry{{Date: "2026-08-24", GameID: 9, IsHome: true, OpponentTeamID: 111, Stat: providers.HittingStats{PlateAppearances: 4, AtBats: 3, Hits: 2, Runs: 1, Average: ".667"}}}, nil
		},
	}
	output, err := service.Pool("B", PlayerPoolOptions{Argument: "Ada Hitter"})
	if err != nil || !strings.Contains(output, "GAME LOG") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	snapshot, err := database.CommandSnapshot("player-game-log", "mlb", "1001")
	if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" || snapshot.Stale {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	service.HitterGameLog = func(int64, int64) ([]providers.HittingGameLogEntry, error) { return nil, errors.New("offline") }
	output, err = service.Pool("B", PlayerPoolOptions{Argument: "Ada Hitter"})
	if err != nil || !strings.Contains(output, "GAME LOG data may be stale") {
		t.Fatalf("stale output=%q err=%v", output, err)
	}
	if unrelated, readErr := database.CommandSnapshot("player-game-log", "mlb", "1002"); readErr != nil || unrelated != nil {
		t.Fatalf("unrelated=%#v err=%v", unrelated, readErr)
	}
}

func TestFantasySelectionAndWaiverFallbacksAreDeterministic(t *testing.T) {
	teams := []store.StoredFantasyTeam{{TeamKey: "a", Name: "Alpha", ManagerName: "Sam"}, {TeamKey: "b", Name: "Beta", ManagerName: "Sammy"}}
	if _, err := selectFantasyTeam(teams, "missing", ""); err == nil || !strings.Contains(err.Error(), "no team") {
		t.Fatalf("missing error=%v", err)
	}
	if _, err := selectFantasyTeam(teams, "", "sam"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error=%v", err)
	}
	owner := "Alpha"
	freeID, ownedID := int64(1), int64(2)
	players := []domain.StoredFantasyPlayer{{YahooPlayerID: &ownedID, Name: "Owned", Owner: &owner, Role: "B"}, {YahooPlayerID: &freeID, Name: "Free", Role: "B"}}
	if analysis.YahooPickupAvailable(players[0]) || !analysis.YahooPickupAvailable(players[1]) {
		t.Fatal("Yahoo ownership gate changed")
	}
}

func TestFantasyDetailStaysWithinCommandRoleAndPreservesLogsWithoutSchedule(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	defer database.Close()
	service := &FantasyService{
		Store: database, League: "mlb.l.1", TeamKey: "mlb.l.1.t.1", Now: func() time.Time { return now }, Mode: terminal.Plain,
		HitterGameLog: func(int64, int64) ([]providers.HittingGameLogEntry, error) {
			return []providers.HittingGameLogEntry{{Date: "2026-08-24", GameID: 9, IsHome: true, OpponentTeamID: 111, Stat: providers.HittingStats{PlateAppearances: 4, AtBats: 3, Hits: 2, Runs: 1, Average: ".667"}}}, nil
		},
		Schedule: func(string) ([]providers.ScheduleGame, error) { return nil, errors.New("offline") },
	}
	output, err := service.Pool("B", PlayerPoolOptions{Argument: "Ada"})
	if err != nil || !strings.Contains(output, "2/3") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, err := service.Pool("B", PlayerPoolOptions{Argument: "Ace Pitcher"}); err == nil || !strings.Contains(err.Error(), "no player") {
		t.Fatalf("opposite-role detail error=%v", err)
	}
}

func TestHitterDetailPreservesEveryCalendarDayBeforeTodaysGameStarts(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	defer database.Close()
	boxscoreCalls := 0
	service := &FantasyService{
		Store: database, Now: func() time.Time { return now },
		Schedule: func(date string) ([]providers.ScheduleGame, error) {
			if date != "2026-08-25" {
				return []providers.ScheduleGame{}, nil
			}
			return []providers.ScheduleGame{{GameID: 9, AwayTeamID: 111, HomeTeamID: 147, DetailedState: "Scheduled"}}, nil
		},
		Boxscore: func(int64) (providers.Boxscore, error) {
			boxscoreCalls++
			return providers.Boxscore{}, nil
		},
	}
	rows, err := service.enrichHitterLogs(domain.StoredFantasyPlayer{Team: "NYY"}, nil)
	if err != nil || len(rows) != 10 || rows[9].Date != "2026-08-25" || rows[9].GameID != 9 || rows[9].Opponent != "BOS" || boxscoreCalls != 0 {
		t.Fatalf("rows=%#v boxscore calls=%d err=%v", rows, boxscoreCalls, err)
	}
}

func TestOptionalFantasyContextIsolationRunningStateAndConfirmedLineupGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	database := fantasyAppStore(t, now)
	defer database.Close()
	service := &FantasyService{Store: database, League: "mlb.l.1", Now: func() time.Time { return now }}
	games := []providers.ScheduleGame{{GameID: 9, AwayTeamID: 147, HomeTeamID: 111}}
	payload, _ := json.Marshal(games)
	if err := database.SaveCommandSnapshot("player_card_schedule", "mlbam", "2026-08-25", "v1", string(payload)); err != nil {
		t.Fatal(err)
	}
	service.Schedule = func(string) ([]providers.ScheduleGame, error) { return nil, errors.New("offline") }
	rows, available := service.optionalSchedule("2026-08-25")
	if !available || len(rows) != 1 {
		t.Fatalf("rows=%#v available=%v", rows, available)
	}
	snapshot, err := database.CommandSnapshot("player_card_schedule", "mlbam", "2026-08-25")
	if err != nil || snapshot == nil || !snapshot.Stale {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	boxscore := providers.Boxscore{Away: providers.BoxscoreTeam{BattingOrder: []int64{1001}, Players: map[int64]providers.BoxscorePlayer{1001: {PersonID: 1001}}}, Home: providers.BoxscoreTeam{Players: map[int64]providers.BoxscorePlayer{}}}
	boxPayload, _ := json.Marshal(boxscore)
	if err := database.SaveCommandSnapshot("player_card_boxscore", "mlbam", "9", "v1", string(boxPayload)); err != nil {
		t.Fatal(err)
	}
	service.Boxscore = func(int64) (providers.Boxscore, error) { return providers.Boxscore{}, errors.New("offline") }
	if row := service.optionalBoxscore(9); row == nil || len(row.Away.BattingOrder) != 1 {
		t.Fatalf("boxscore fallback=%#v", row)
	}
	boxSnapshot, err := database.CommandSnapshot("player_card_boxscore", "mlbam", "9")
	if err != nil || boxSnapshot == nil || !boxSnapshot.Stale {
		t.Fatalf("box snapshot=%#v err=%v", boxSnapshot, err)
	}
	if row := service.optionalBoxscore(10); row != nil {
		t.Fatalf("missing optional boxscore=%#v", row)
	}
	if _, err := database.StartSyncRun(store.SyncLive, store.OriginManual); err != nil {
		t.Fatal(err)
	}
	if err := service.missingYahooAvailability("h"); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("running error=%v", err)
	}
	original := []providers.LineupPlayer{{PersonID: 1001, FullName: "Ada Hitter"}}
	games[0].AwayLineup = append([]providers.LineupPlayer(nil), original...)
	overlayLineups(games, []providers.DailyLineup{{Confirmed: true, AwayTeam: "NYY", HomeTeam: "BOS", AwayPlayers: []string{"Unknown"}}}, nil)
	if len(games[0].AwayLineup) != 1 || games[0].AwayLineup[0].PersonID != 1001 {
		t.Fatalf("incomplete lineup replaced schedule evidence: %#v", games[0].AwayLineup)
	}
}

func TestFantasySortAliasesAndRecentProjectionBlend(t *testing.T) {
	one, two := int64(1), int64(2)
	low, high := .300, .400
	players := []domain.StoredFantasyPlayer{{YahooPlayerID: &one, Name: "Rank One", Role: "B", Rank: &one, HittingAdvanced: [8]*float64{&low}}, {YahooPlayerID: &two, Name: "Rank Two", Role: "B", Rank: &two, HittingAdvanced: [8]*float64{&high}}}
	sortFantasyPool(players, "xwoba")
	if players[0].Name != "Rank Two" {
		t.Fatalf("xwoba sort=%#v", players)
	}
	sortFantasyPool(players, "unknown")
	if players[0].Name != "Rank One" {
		t.Fatalf("rank fallback=%#v", players)
	}
	line := nextProjectionLine(domain.StoredFantasyPlayer{Role: "B"}, store.ProjectionRow{PA: 100, R: 10, AVG: .250, OBP: .300, SLG: .400}, []domain.PlayerGameLog{{Line: "PA 10 AB 8 H 4 R 4 HR 0 RBI 1 SB 0 AVG .500 OBP .600 OPS 1.000"}})
	if !strings.Contains(line, "R   4") || !strings.Contains(line, "AVG 0.325") {
		t.Fatalf("recent blend line=%q", line)
	}
}

func TestRecentProjectionWindowsIgnoreGamesOlderThanTheirThreshold(t *testing.T) {
	hitter := domain.StoredFantasyPlayer{Role: "B"}
	hitterProjection := store.ProjectionRow{PA: 100, R: 10, AVG: .250, OBP: .300, SLG: .400}
	hitterRecent := []domain.PlayerGameLog{
		{Date: "2026-08-24", Line: "PA 10 AB 8 H 2 R 1 HR 0 RBI 1 SB 0 AVG .250 OBP .300 OPS .700"},
		{Date: "2026-08-25", Line: "PA 10 AB 8 H 2 R 1 HR 0 RBI 1 SB 0 AVG .250 OBP .300 OPS .700"},
	}
	hitterWithOldOutlier := append([]domain.PlayerGameLog{{Date: "2026-08-01", Line: "PA 100 AB 80 H 80 R 99 HR 50 RBI 99 SB 20 AVG 1.000 OBP 1.000 OPS 4.000"}}, hitterRecent...)
	hitterLine := nextProjectionLine(hitter, hitterProjection, hitterRecent)
	if got := nextProjectionLine(hitter, hitterProjection, hitterWithOldOutlier); got != hitterLine || !strings.HasPrefix(got, "NEXT20PA") {
		t.Fatalf("hitter line with old outlier=%q want=%q", got, hitterLine)
	}

	starter := domain.StoredFantasyPlayer{Role: "P", Positions: "SP"}
	pitcherProjection := store.ProjectionRow{IP: 100, W: 10, K: 100, ERA: 3, WHIP: 1.1}
	starterRecent := []domain.PlayerGameLog{
		{Date: "2026-08-18", Line: "IP 5.0 QS 0 W 0 SV 0 K 5 ERA 3.00 WHIP 1.00"},
		{Date: "2026-08-25", Line: "IP 6.0 QS 1 W 1 SV 0 K 6 ERA 3.00 WHIP 1.00"},
	}
	starterWithOldOutlier := append([]domain.PlayerGameLog{{Date: "2026-08-01", Line: "IP 9.0 QS 1 W 1 SV 0 K 99 ERA 99.00 WHIP 9.00"}}, starterRecent...)
	starterLine := nextProjectionLine(starter, pitcherProjection, starterRecent)
	if got := nextProjectionLine(starter, pitcherProjection, starterWithOldOutlier); got != starterLine || !strings.HasPrefix(got, "NEXT10IP") {
		t.Fatalf("starter line with old outlier=%q want=%q", got, starterLine)
	}

	reliever := domain.StoredFantasyPlayer{Role: "P", Positions: "RP"}
	relieverRecent := []domain.PlayerGameLog{
		{Date: "2026-08-23", Line: "IP 1.1 QS 0 W 0 SV 1 K 2 ERA 0.00 WHIP .75"},
		{Date: "2026-08-25", Line: "IP 2.0 QS 0 W 1 SV 0 K 3 ERA 0.00 WHIP .50"},
	}
	relieverWithOldOutlier := append([]domain.PlayerGameLog{{Date: "2026-08-01", Line: "IP 4.0 QS 0 W 0 SV 0 K 99 ERA 99.00 WHIP 9.00"}}, relieverRecent...)
	relieverLine := nextProjectionLine(reliever, pitcherProjection, relieverRecent)
	if got := nextProjectionLine(reliever, pitcherProjection, relieverWithOldOutlier); got != relieverLine || !strings.HasPrefix(got, "NEXT03IP") {
		t.Fatalf("reliever line with old outlier=%q want=%q", got, relieverLine)
	}
}
