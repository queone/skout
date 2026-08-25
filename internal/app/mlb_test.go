package app

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

type appClock struct{ value time.Time }

func (clock appClock) Now() time.Time { return clock.value }

type flushBuffer struct {
	bytes.Buffer
	flushed bool
}

func (output *flushBuffer) Flush() error {
	output.flushed = true
	return nil
}

type flushAwareReader struct {
	source     *strings.Reader
	prompt     *flushBuffer
	sawFlushed bool
}

func (input *flushAwareReader) Read(buffer []byte) (int, error) {
	input.sawFlushed = input.prompt.flushed
	return input.source.Read(buffer)
}

func TestMLBScheduleDebugTimingUsesInjectedClockAndWriter(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	measured := now
	var output bytes.Buffer
	cacheState := cache.Hit
	service := &MLBService{
		Cache: cache.At(t.TempDir()), Debug: true, DebugOutput: &output,
		MeasureNow: func() time.Time {
			value := measured
			measured = measured.Add(5 * time.Millisecond)
			return value
		},
		ScheduleCached: func(string, *cache.Disk) (providers.ScheduleCacheResult, error) {
			return providers.ScheduleCacheResult{Games: []providers.ScheduleGame{{GameID: 1}}, CacheState: cacheState}, nil
		},
		Schedule: func(string) ([]providers.ScheduleGame, error) {
			return []providers.ScheduleGame{{GameID: 2}}, nil
		},
	}
	if games := service.cachedSchedule("2026-05-15", false); len(games) != 1 || games[0].GameID != 1 {
		t.Fatalf("cached games=%#v", games)
	}
	if games := service.cachedSchedule("2026-05-15", true); len(games) != 1 || games[0].GameID != 2 {
		t.Fatalf("uncached games=%#v", games)
	}
	cacheState = cache.Missing
	if games := service.cachedSchedule("2026-05-15", false); len(games) != 1 || games[0].GameID != 1 {
		t.Fatalf("cache-miss games=%#v", games)
	}
	want := "skout debug: mlb schedule fetch took 5ms (Hit)\nskout debug: mlb schedule fetch took 5ms (uncached)\nskout debug: mlb schedule fetch took 5ms (Miss)\n"
	if output.String() != want {
		t.Fatalf("debug output=%q want=%q", output.String(), want)
	}
}

func TestMLBOptionalOwnershipConfigMatchesFrozenFallback(t *testing.T) {
	if key := optionalCurrentTeamKey(func() (config.Config, error) { return config.Config{}, errors.New("malformed") }); key != "" {
		t.Fatalf("fallback key=%q", key)
	}
	if key := optionalCurrentTeamKey(func() (config.Config, error) { return config.Config{CurrentTeamKey: "1.t.2"}, nil }); key != "1.t.2" {
		t.Fatalf("configured key=%q", key)
	}
}

func TestMLBTeamsUsesFreshSnapshotsForceAndStaleFallback(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	database, err := store.OpenAtWithClock(filepath.Join(t.TempDir(), "mlb.db"), appClock{now})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	directoryCalls, rosterCalls, standingCalls := 0, 0, 0
	fail := false
	service := &MLBService{Store: database, Now: func() time.Time { return now }, Location: time.UTC, Mode: terminal.Plain,
		Directory: func(int64) ([]providers.TeamDirectoryEntry, error) {
			directoryCalls++
			if fail {
				return nil, errors.New("directory down")
			}
			return []providers.TeamDirectoryEntry{{TeamID: 147, Name: "New York Yankees", LocationName: "New York", ClubName: "Yankees", Abbreviation: "NYY", LeagueID: 103}}, nil
		},
		Standings: func(int64) ([]providers.TeamStanding, error) {
			standingCalls++
			if fail {
				return nil, errors.New("standings down")
			}
			return []providers.TeamStanding{{TeamID: 147, LeagueID: 103, Wins: 30, Losses: 20}}, nil
		},
		Roster: func(int64) ([]providers.RosterMember, error) {
			rosterCalls++
			if fail {
				return nil, errors.New("roster down")
			}
			return []providers.RosterMember{{PersonID: 1, FullName: "Player One", Position: "OF", PrimaryType: "H", Status: "A"}}, nil
		},
		Schedule: func(string) ([]providers.ScheduleGame, error) { return nil, nil }, Hitting: func(int64, string) ([]providers.BulkHittingSplit, error) { return nil, nil }, Pitching: func(int64, string) ([]providers.BulkPitchingSplit, error) { return nil, nil }, CurrentOdds: func(time.Time) (providers.ESPNSlateLines, error) { return providers.ESPNSlateLines{}, nil }, FutureOdds: func(string) ([]providers.OddsSharkGameLine, error) { return nil, nil }}
	output, err := service.Teams("nyy", false)
	if err != nil || !strings.Contains(output, "NYY - New York Yankees (30-20)") {
		t.Fatalf("first output=%s err=%v", output, err)
	}
	if directoryCalls != 1 || rosterCalls != 1 || standingCalls != 1 {
		t.Fatalf("first calls=%d/%d/%d", directoryCalls, rosterCalls, standingCalls)
	}
	output, err = service.Teams("NYY", false)
	if err != nil || !strings.Contains(output, "NYY - New York Yankees") {
		t.Fatalf("fresh output=%s err=%v", output, err)
	}
	if directoryCalls != 1 || rosterCalls != 1 || standingCalls != 1 {
		t.Fatalf("fresh cache calls=%d/%d/%d", directoryCalls, rosterCalls, standingCalls)
	}
	fail = true
	output, err = service.Teams("NYY", true)
	if err != nil || !strings.Contains(output, "roster is stale") {
		t.Fatalf("stale output=%s err=%v", output, err)
	}
	if directoryCalls != 2 || rosterCalls != 2 || standingCalls != 2 {
		t.Fatalf("force calls=%d/%d/%d", directoryCalls, rosterCalls, standingCalls)
	}
}

func TestMLBResolutionAggregationSlateAndTimeAreDeterministic(t *testing.T) {
	teams := []domain.Team{{ID: 1, Name: "New York Yankees", Location: "New York", ClubName: "Yankees", Abbreviation: "NYY", LeagueID: 103}, {ID: 2, Name: "New York Mets", Location: "New York", ClubName: "Mets", Abbreviation: "NYM", LeagueID: 104}}
	service := &MLBService{InputTerminal: false, PromptTerminal: false}
	if resolved, err := service.resolveTeams(teams, "mets"); err != nil || resolved[0].ID != 2 {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	if _, err := service.resolveTeams(teams, "new york"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguity=%v", err)
	}
	hitting := []providers.BulkHittingSplit{{Player: providers.BulkPlayer{PersonID: 10, FullName: "H"}, Team: providers.BulkTeam{TeamID: 1}, Stat: providers.HittingStats{PlateAppearances: 12, AtBats: 10, Hits: 4, Walks: 1, HitByPitch: 1, TotalBases: 7}}}
	pitching := []providers.BulkPitchingSplit{{Player: providers.BulkPlayer{PersonID: 11, FullName: "P"}, Team: providers.BulkTeam{TeamID: 1}, Stat: providers.PitchingStats{InningsPitched: "6.2", EarnedRuns: 2, HitsAllowed: 4, Walks: 2}}}
	totals := aggregateTotals(teams[:1], hitting, pitching)
	if len(totals) != 1 || totals[0].Batting.BattingAverage != .4 || totals[0].Pitching.InningsPitched != 20.0/3.0 {
		t.Fatalf("totals=%#v", totals)
	}
	game := providers.ScheduleGame{GameID: 7, GameDate: "2026-05-15T23:05:00Z", AwayTeamID: 1, AwayTeamName: "New York Yankees", HomeTeamID: 2, HomeTeamName: "New York Mets", AwayProbablePitcherName: "Away Ace", HomeProbablePitcherName: "Home Ace"}
	current := providers.ESPNSlateLines{Games: []providers.ESPNGameLine{{AwayTeam: "New-York Yankees", HomeTeam: "New York Mets", AwayMoneyline: 120, HomeMoneyline: -130, Quoted: true}}}
	rows := composeSlate([]providers.ScheduleGame{game}, teams, "2026-05-15", map[int64]string{7: "2026-05-15"}, current, nil, time.FixedZone("EDT", -4*60*60))
	if len(rows) != 1 || rows[0].GameTime != "7:05p" || rows[0].Date != "May 15 Fri" || rows[0].WinProbability == nil {
		t.Fatalf("rows=%#v", rows)
	}
}

func TestMLBSlateDoubleheaderOccurrenceUsesProviderOrderWithoutMutatingGames(t *testing.T) {
	games := []providers.ScheduleGame{
		{GameID: 2, GameDate: "2026-05-15T23:05:00Z", AwayTeamName: "Away", HomeTeamName: "Home"},
		{GameID: 1, GameDate: "2026-05-15T18:05:00Z", AwayTeamName: "Away", HomeTeamName: "Home"},
	}
	current := providers.ESPNSlateLines{Games: []providers.ESPNGameLine{
		{AwayTeam: "Away", HomeTeam: "Home", AwayMoneyline: 110, HomeMoneyline: -120, Quoted: true},
		{AwayTeam: "Away", HomeTeam: "Home", AwayMoneyline: 210, HomeMoneyline: -220, Quoted: true},
	}}
	firstProbability, _ := normalizedProbability(110, -120)
	secondProbability, _ := normalizedProbability(210, -220)
	rows := composeSlate(games, nil, "2026-05-15", map[int64]string{1: "2026-05-15", 2: "2026-05-15"}, current, nil, time.UTC)
	if len(rows) != 2 || rows[0].GameID != 1 || rows[1].GameID != 2 || rows[0].WinProbability == nil || rows[1].WinProbability == nil {
		t.Fatalf("rows=%#v", rows)
	}
	if *rows[0].WinProbability != secondProbability || *rows[1].WinProbability != firstProbability {
		t.Fatalf("probabilities=%v/%v want=%v/%v", *rows[0].WinProbability, *rows[1].WinProbability, secondProbability, firstProbability)
	}
	if games[0].GameID != 2 || games[1].GameID != 1 {
		t.Fatalf("input games were reordered: %#v", games)
	}
	if got := gameOfficialDate(providers.ScheduleGame{GameDate: "short"}, nil); got != "short" {
		t.Fatalf("short fallback date=%q", got)
	}
}

func TestMLBInteractiveResolutionFlushesPromptBeforeReading(t *testing.T) {
	teams := []domain.Team{{ID: 1, Name: "New York Yankees", Location: "New York", ClubName: "Yankees", Abbreviation: "NYY"}, {ID: 2, Name: "New York Mets", Location: "New York", ClubName: "Mets", Abbreviation: "NYM"}}
	prompt := &flushBuffer{}
	input := &flushAwareReader{source: strings.NewReader("2\n"), prompt: prompt}
	service := &MLBService{Input: input, Prompt: prompt, InputTerminal: true, PromptTerminal: true}
	resolved, err := service.resolveTeams(teams, "new york")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].ID != 2 {
		t.Fatalf("resolved=%#v", resolved)
	}
	if !input.sawFlushed {
		t.Fatal("selection input was read before the prompt was flushed")
	}
	want := "team: \"new york\" matches multiple MLB clubs:\n  1) NYY — New York Yankees\n  2) NYM — New York Mets\nSelect a team [1-2]: "
	if prompt.String() != want {
		t.Fatalf("prompt=%q want=%q", prompt.String(), want)
	}
}

func TestCachedSnapshotRetainsLastCompletePayloadAfterFailure(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	database, err := store.OpenAtWithClock(filepath.Join(t.TempDir(), "cache.db"), appClock{now})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	calls := 0
	fetch := func() ([]int, error) { calls++; return []int{1, 2}, nil }
	value, stale, refreshed, err := cached(database, now, "d", "s", "x", time.Minute, false, fetch)
	if err != nil || stale || !refreshed || len(value) != 2 {
		t.Fatalf("first=%v/%v/%v/%v", value, stale, refreshed, err)
	}
	value, stale, refreshed, err = cached(database, now.Add(10*time.Second), "d", "s", "x", time.Minute, false, func() ([]int, error) { calls++; return nil, errors.New("down") })
	if err != nil || stale || refreshed || calls != 1 {
		t.Fatalf("fresh=%v/%v/%v calls=%d err=%v", value, stale, refreshed, calls, err)
	}
	value, stale, refreshed, err = cached(database, now.Add(2*time.Minute), "d", "s", "x", time.Minute, false, func() ([]int, error) { return nil, errors.New("down") })
	if err != nil || !stale || refreshed || len(value) != 2 {
		t.Fatalf("fallback=%v/%v/%v err=%v", value, stale, refreshed, err)
	}
}
