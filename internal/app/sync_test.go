package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
)

type syncYahooFixture struct {
	failFreeAgents bool
}

func (fixture syncYahooFixture) LeagueSettings(league string) (providers.LeagueSettings, error) {
	week := 1
	return providers.LeagueSettings{
		League:          domain.League{LeagueKey: league, Name: "League", Season: 2026, NumTeams: 2, ScoringType: domain.ScoringHeadToHead},
		CurrentWeek:     &week,
		Categories:      []providers.StatCategory{{StatID: 7, Abbreviation: "R", Name: "Runs", SortOrder: 1, Sequence: 1}},
		RosterPositions: []providers.RosterPosition{{Position: domain.PositionOutfield, Count: 1}},
	}, nil
}

func (fixture syncYahooFixture) Standings(league string) ([]domain.FantasyTeam, error) {
	return []domain.FantasyTeam{
		{TeamKey: league + ".t.1", LeagueKey: league, TeamID: 1, Name: "Operators", Rank: 1},
		{TeamKey: league + ".t.2", LeagueKey: league, TeamID: 2, Name: "Opponents", Rank: 2},
	}, nil
}

func (fixture syncYahooFixture) LeagueRosters(league string, _ []string) (providers.LeagueRosters, error) {
	return providers.LeagueRosters{
		Players: []domain.FantasyPlayer{
			{YahooPlayerID: 1, Name: "One", MLBTeam: "NYY", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}},
			{YahooPlayerID: 2, Name: "Two", MLBTeam: "BOS", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}},
		},
		Slots: []domain.FantasyRosterSlot{
			{TeamKey: league + ".t.1", YahooPlayerID: 1, SlotPosition: domain.PositionOutfield},
			{TeamKey: league + ".t.2", YahooPlayerID: 2, SlotPosition: domain.PositionOutfield},
		},
	}, nil
}

func (fixture syncYahooFixture) FreeAgents(string) ([]domain.FantasyPlayer, error) {
	if fixture.failFreeAgents {
		return nil, errors.New("available players are incomplete")
	}
	return []domain.FantasyPlayer{{YahooPlayerID: 3, Name: "Three", MLBTeam: "TB", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}}}, nil
}

func (fixture syncYahooFixture) Scoreboard(league string, week *int) ([]domain.Matchup, error) {
	return []domain.Matchup{{Week: *week, Teams: [2]domain.MatchupTeam{{TeamKey: league + ".t.1"}, {TeamKey: league + ".t.2"}}}}, nil
}

func (fixture syncYahooFixture) RosterWeekStats(team string, week int) (domain.RosterWeekStats, error) {
	return domain.RosterWeekStats{TeamKey: team, Week: week}, nil
}

func TestSyncCompleteDegradedAndDurableSelectionWorkflow(t *testing.T) {
	root := t.TempDir()
	database, err := store.OpenAt(filepath.Join(root, "skout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	configPath := filepath.Join(root, "config.json")
	var output syncFlushBuffer
	service := SyncService{
		Store: database, Yahoo: syncYahooFixture{}, ConfigPath: configPath,
		RuntimeDirectory: filepath.Join(root, "runtime"), Output: &output,
		Input: strings.NewReader(""), Prompt: io.Discard, Origin: store.OriginManual,
		CanonicalLeague: func(string) (string, error) { return "mlb.l.1", nil },
		Steps: []SyncStep{
			{Source: "good", Item: "rows", Scope: "2026", Run: func(*store.Store) (SyncStepResult, error) { return SyncStepResult{Count: 4}, nil }},
			{Source: "optional", Item: "rows", Scope: "2026", Run: func(*store.Store) (SyncStepResult, error) {
				return SyncStepResult{}, errors.New("provider unavailable")
			}},
		},
	}
	summary, err := service.Run("1", "operators")
	if err != nil || !strings.Contains(summary, "degraded success: 2 steps succeeded, 1 failed") || !strings.Contains(output.String(), "Sync started") || !strings.Contains(output.String(), "optional rows: fetching -> failed") {
		t.Fatalf("summary=%q output=%q err=%v", summary, output.String(), err)
	}
	if output.flushes != 7 {
		t.Fatalf("progress flushes=%d want=7", output.flushes)
	}
	settings, err := config.ReadAt(configPath)
	if err != nil || settings.CurrentLeague != "mlb.l.1" || settings.CurrentTeamKey != "mlb.l.1.t.1" {
		t.Fatalf("config=%#v err=%v", settings, err)
	}
	if snapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:1"); err != nil || snapshot == nil {
		t.Fatalf("scoreboard=%#v err=%v", snapshot, err)
	}
	run, err := database.LatestSyncRun(store.SyncLive)
	if err != nil || run == nil || run.Status != "complete" || run.Counts["yahoo_public_fantasy"] != 3 || run.Counts["good_rows"] != 4 {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	status, err := store.InspectStatusAt(database.Path(), "mlb.l.1")
	if err != nil || status.LastRunStatus == nil || *status.LastRunStatus != "degraded" || status.ProviderFailureCount != 0 || status.YahooIdentityCount != 3 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestSyncYahooFailureRetainsPriorSnapshotAndConfiguration(t *testing.T) {
	root := t.TempDir()
	database, err := store.OpenAt(filepath.Join(root, "skout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	configPath := filepath.Join(root, "config.json")
	service := SyncService{Store: database, Yahoo: syncYahooFixture{}, ConfigPath: configPath, RuntimeDirectory: filepath.Join(root, "runtime"), Output: io.Discard, Input: strings.NewReader(""), Origin: store.OriginManual}
	if _, err := service.Run("mlb.l.1", "Operators"); err != nil {
		t.Fatal(err)
	}
	prior, err := database.FantasyPlayers("mlb.l.1")
	if err != nil {
		t.Fatal(err)
	}
	priorSnapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:1")
	if err != nil || priorSnapshot == nil {
		t.Fatalf("prior snapshot=%#v err=%v", priorSnapshot, err)
	}
	service.Yahoo = syncYahooFixture{failFreeAgents: true}
	if _, err := service.Run("mlb.l.1", "Operators"); err == nil || !strings.Contains(err.Error(), "every provider failed") {
		t.Fatalf("failure=%v", err)
	}
	after, err := database.FantasyPlayers("mlb.l.1")
	if err != nil || len(after) != len(prior) || after[0].Name != prior[0].Name {
		t.Fatalf("prior=%#v after=%#v err=%v", prior, after, err)
	}
	afterSnapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:1")
	if err != nil || afterSnapshot == nil || afterSnapshot.Payload != priorSnapshot.Payload || !afterSnapshot.LastSuccessfulAt.Equal(priorSnapshot.LastSuccessfulAt) {
		t.Fatalf("prior snapshot=%#v after=%#v err=%v", priorSnapshot, afterSnapshot, err)
	}
	settings, err := config.ReadAt(configPath)
	if err != nil || settings.CurrentTeamKey != "mlb.l.1.t.1" {
		t.Fatalf("config=%#v err=%v", settings, err)
	}
	status, err := store.InspectStatusAt(database.Path(), "mlb.l.1")
	if err != nil || status.LastRunStatus == nil || *status.LastRunStatus != "failed" || status.ProviderFailureCount != 1 || status.ProviderLastError == nil {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestPrimaryTeamSelectionFlushesPromptAndReportsNoninteractiveErrors(t *testing.T) {
	teams := []domain.FantasyTeam{{TeamKey: "l.t.1", Name: "New York"}, {TeamKey: "l.t.2", Name: "New Jersey"}}
	for requested, want := range map[string]string{"l.t.1": "l.t.1", "NEW JERSEY": "l.t.2", "york": "l.t.1"} {
		selected, err := SelectPrimaryTeam(teams, requested, false, nil, nil)
		if err != nil || selected != want {
			t.Fatalf("requested=%q selected=%q want=%q err=%v", requested, selected, want, err)
		}
	}
	if _, err := SelectPrimaryTeam(teams, "new", false, nil, nil); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error=%v", err)
	}
	prompt := &syncFlushBuffer{}
	selected, err := SelectPrimaryTeam(teams, "missing", true, strings.NewReader("2\n"), prompt)
	if err != nil || selected != "l.t.2" || prompt.flushes != 1 || !strings.Contains(prompt.String(), "Choice:") {
		t.Fatalf("selected=%q prompt=%q flushes=%d err=%v", selected, prompt.String(), prompt.flushes, err)
	}
}

func TestSyncRejectsConcurrentLockAndRetainsLockFile(t *testing.T) {
	root := t.TempDir()
	guard, err := acquireSyncGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.release()
	if _, err := acquireSyncGuard(root); err == nil || !strings.Contains(err.Error(), "another synchronization") {
		t.Fatalf("second lock error=%v", err)
	}
	guard.release()
	reacquired, err := acquireSyncGuard(root)
	if err != nil {
		t.Fatalf("reacquire=%v", err)
	}
	reacquired.release()
	matches, err := filepath.Glob(filepath.Join(root, "sync.lock"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("lock file matches=%v err=%v", matches, err)
	}
}

func TestBuildSyncStepsUsesFrozenOrderAndExcludesCommandTimeSources(t *testing.T) {
	steps := buildSyncSteps(2026, time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC), syncSourceSet{})
	labels := make([]string, 0, len(steps)+1)
	labels = append(labels, "yahoo_public/fantasy")
	for _, step := range steps {
		labels = append(labels, step.Source+"/"+step.Item)
	}
	want := []string{
		"yahoo_public/fantasy", "mlb/hitting", "mlb/pitching",
		"mlb_history/2021_hitting", "mlb_history/2021_pitching",
		"mlb_history/2022_hitting", "mlb_history/2022_pitching",
		"mlb_history/2023_hitting", "mlb_history/2023_pitching",
		"mlb_history/2024_hitting", "mlb_history/2024_pitching",
		"mlb_history/2025_hitting", "mlb_history/2025_pitching",
		"mlb/40man_rosters", "savant/batting", "savant/pitching",
		"fangraphs/snapshot", "fantasypros/ecr", "espn/mlb_current_odds",
	}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("step order=%v want=%v", labels, want)
	}
	for _, label := range labels {
		if strings.Contains(label, "rotowire") || strings.Contains(label, "oddsshark") {
			t.Fatalf("command-time source entered foreground sync: %s", label)
		}
	}
}

func TestPitchingSyncWritesMergeOnlySuccessfulQualityStartCounts(t *testing.T) {
	rows := []providers.BulkPitchingSplit{
		{Player: providers.BulkPlayer{PersonID: 1, FullName: "One"}, Team: providers.BulkTeam{TeamID: 147}, Stat: providers.PitchingStats{GamesPitched: 9, GamesStarted: 8, QualityStarts: 3, InningsPitched: "51.2"}},
		{Player: providers.BulkPlayer{PersonID: 2, FullName: "Two"}, Team: providers.BulkTeam{TeamID: 111}, Stat: providers.PitchingStats{GamesPitched: 7, GamesStarted: 6, QualityStarts: 4, InningsPitched: "40.0"}},
	}
	writes := pitchingSyncWrites(rows, map[int64]int64{1: 6})
	if writes[0].QualityStarts != 6 || writes[1].QualityStarts != 4 || writes[0].InningsOuts != 155 || writes[0].TeamAbbreviation != "NYY" {
		t.Fatalf("writes=%#v", writes)
	}
}

func TestMLBRosterStepIsolatesTeamsRetainsFailuresAndRespectsRowFreshness(t *testing.T) {
	database, err := store.OpenAt(filepath.Join(t.TempDir(), "rosters.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	teams := make([]providers.TeamDirectoryEntry, 0, 30)
	for index := range 30 {
		teams = append(teams, providers.TeamDirectoryEntry{TeamID: int64(1000 + index), Abbreviation: fmt.Sprintf("T%02d", index)})
	}
	if err := database.ReplaceMLBRoster("T00", []store.RosterWrite{{MLBAMID: 99, Name: "Prior", Position: "OF", PrimaryType: "H", Status: "A"}}); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	sources := syncSourceSet{
		Force:        true,
		MLBDirectory: func(int64) ([]providers.TeamDirectoryEntry, error) { return teams, nil },
		MLBRoster: func(teamID int64) ([]providers.RosterMember, error) {
			fetches++
			if teamID == 1000 {
				return nil, errors.New("team unavailable")
			}
			return []providers.RosterMember{{PersonID: teamID + 10000, FullName: fmt.Sprintf("Player %d", teamID), Position: "OF", PrimaryType: "H", Status: "A"}}, nil
		},
	}
	result, err := mlbRosterStep(2026, sources).Run(database)
	if err != nil || !result.Degraded || result.Count != 29 || fetches != 30 || !strings.Contains(result.Detail, "29 teams succeeded, 1 failed") {
		t.Fatalf("result=%#v fetches=%d err=%v", result, fetches, err)
	}
	prior, err := database.MLBRoster("T00")
	if err != nil || len(prior) != 1 || prior[0].Name != "Prior" {
		t.Fatalf("prior roster=%#v err=%v", prior, err)
	}
	failed, err := database.SyncRowState("mlb", "40man_team", "2026", "team", "T00")
	if err != nil || failed == nil || failed.Status != store.SyncStateFailed || failed.ErrorMessage == "" {
		t.Fatalf("failed row=%#v err=%v", failed, err)
	}

	sources.Force = false
	sources.MLBRoster = func(teamID int64) ([]providers.RosterMember, error) {
		fetches++
		return []providers.RosterMember{{PersonID: teamID + 20000, FullName: "Unexpected", Position: "OF", PrimaryType: "H", Status: "A"}}, nil
	}
	result, err = mlbRosterStep(2026, sources).Run(database)
	if err != nil || result.Count != 1 || fetches != 31 {
		t.Fatalf("freshness result=%#v fetches=%d err=%v", result, fetches, err)
	}
	duplicate := append([]providers.TeamDirectoryEntry(nil), teams...)
	duplicate[len(duplicate)-1] = duplicate[0]
	sources.MLBDirectory = func(int64) ([]providers.TeamDirectoryEntry, error) { return duplicate, nil }
	if _, err := mlbRosterStep(2026, sources).Run(database); err == nil || !strings.Contains(err.Error(), "unique teams") {
		t.Fatalf("duplicate directory error=%v", err)
	}
}

func TestProductDocumentationMatchesTheExecutablePublicSurface(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	read := func(name string) string {
		t.Helper()
		payload, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}
	headings := func(document string) []string {
		var result []string
		for line := range strings.SplitSeq(document, "\n") {
			if strings.HasPrefix(line, "## ") {
				result = append(result, line)
			}
		}
		return result
	}

	readme := read("README.md")
	wantREADMEHeadings := []string{"## Why", "## Overview", "## Setup", "## Usage", "## Example Use Case", "## Data Sources", "## Building from Source", "## Governance"}
	if got := headings(readme); !reflect.DeepEqual(got, wantREADMEHeadings) {
		t.Fatalf("README headings=%v want=%v", got, wantREADMEHeadings)
	}
	forbiddenNarration := regexp.MustCompile(`(?i)\b(migration|port|frozen|version[- ]axis|deferred)\b`)
	if match := forbiddenNarration.FindString(readme); match != "" {
		t.Fatalf("README contains non-product narration %q", match)
	}
	for _, evidence := range []string{"public-only", "never changes a Yahoo roster", "./build.sh", "Requires Go", "skout sync", "skout st", "skout reset", "skout t", "skout tt", "skout sp", "skout i", "skout fetch", "accepts only `y` or `yes`", "preserving configuration"} {
		if !strings.Contains(readme, evidence) {
			t.Fatalf("README lacks %q", evidence)
		}
	}
	for _, command := range []string{"skout m ", "skout r ", "skout rt", "skout h ", "skout p "} {
		if !strings.Contains(readme, command) {
			t.Fatalf("README lacks active command %q", command)
		}
	}
	plan := read("plan.md")
	wantPlanHeadings := []string{"## Product Direction", "## Ideas To Explore"}
	if got := headings(plan); !reflect.DeepEqual(got, wantPlanHeadings) {
		t.Fatalf("plan headings=%v want=%v", got, wantPlanHeadings)
	}
	if forbiddenNarration.MatchString(plan) {
		t.Fatal("plan contains delivery-history narration")
	}
	idea := regexp.MustCompile(`^- IE[0-9]+: `)
	for line := range strings.SplitSeq(plan, "\n") {
		if strings.HasPrefix(line, "- ") && !idea.MatchString(line) {
			t.Fatalf("plan contains non-IE idea entry %q", line)
		}
	}
	if strings.Contains(plan, "IE4") {
		t.Fatal("plan retains delivered IE4")
	}

	architecture := read("arch.md")
	for _, evidence := range []string{"public endpoints only", "schema version 6", "Credentials", "roster mutation", "background", "fantasy matchup", "database-operation lock", "skout.db-wal", "Every advertised command now has an executable Go path", "parity review", "already archived"} {
		if !strings.Contains(architecture, evidence) {
			t.Fatalf("architecture lacks boundary evidence %q", evidence)
		}
	}
	if strings.Contains(strings.ToLower(architecture), "authenticated yahoo") {
		t.Fatal("architecture claims that Yahoo authentication is required")
	}

	provenance := read("internal/providers/testdata/PROVENANCE.md")
	if !strings.Contains(provenance, "13d8141eef8e1f36b295d651a91a1298e145f0d6") || !strings.Contains(provenance, "synthetic or scrubbed") {
		t.Fatal("fixture provenance is incomplete")
	}
}

func TestRepositorySourcesAvoidExternalReferenceRuntimeAndDeveloperPaths(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	developerPath := regexp.MustCompile(`(?:/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|[A-Za-z]:\\Users\\[^\\]+\\)`)
	referenceName := "skout" + "-rust"
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if developerPath.Match(payload) {
			t.Errorf("developer-specific absolute path in %s", path)
		}
		if filepath.Ext(path) == ".go" && strings.Contains(string(payload), referenceName) {
			t.Errorf("Go source depends on external reference name in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type syncFlushBuffer struct {
	bytes.Buffer
	flushes int
}

func (buffer *syncFlushBuffer) Flush() error {
	buffer.flushes++
	return nil
}
