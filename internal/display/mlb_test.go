package display

import (
	"os"
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

func TestRosterPlainOutputMatchesFrozenGoldenAndColorPreservesWidth(t *testing.T) {
	rank := int64(25)
	owner := "Sluggers"
	players := []domain.RosterPlayer{
		{TeamAbbreviation: "LAA", MLBAMID: 1, Name: "Active Hitter", Position: "OF", PrimaryType: "H", Status: "A", GameStatus: "Final @ TEX", JerseyNumber: "9", EligiblePositions: "OF", BatSide: "R", YahooRank: &rank, Owner: &owner, InYahooPool: true, PlateAppearances: 200, OnBasePercentage: .350, Runs: 30, HomeRuns: 8, RunsBattedIn: 25, StolenBases: 4, BattingAverage: .275},
		{TeamAbbreviation: "LAA", MLBAMID: 2, Name: "Injured Pitcher", Position: "P", PrimaryType: "P", Status: "D10", InjuryStatus: "IL10", GameStatus: "Final @ TEX", JerseyNumber: "40", EligiblePositions: "SP,P", PitchHand: "L", InYahooPool: true, InningsPitched: 65.1, QualityStarts: 12, Wins: 5, Strikeouts: 70, EarnedRunAverage: 3.20, WHIP: 1.10},
		{TeamAbbreviation: "LAA", MLBAMID: 3, Name: "Two Way", Position: "TWP", PrimaryType: "H", Status: "A", JerseyNumber: "17"},
		{TeamAbbreviation: "LAA", MLBAMID: 3, Name: "Two Way", Position: "TWP", PrimaryType: "P", Status: "A", JerseyNumber: "17"},
	}
	plain := RenderRosters([]RosterGroup{{Heading: "LAA - Los Angeles Angels (70-50)", Players: players}}, nil, terminal.Plain)
	want, err := os.ReadFile("testdata/team-roster.txt")
	if err != nil {
		t.Fatal(err)
	}
	if plain != string(want) {
		t.Fatalf("plain output differs\nGOT:\n%s\nWANT:\n%s", plain, want)
	}
	colored := RenderRosters([]RosterGroup{{Heading: "LAA - Los Angeles Angels (70-50)", Players: players}}, nil, terminal.Color)
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("color output has no ANSI")
	}
	plainLines, colorLines := strings.Split(plain, "\n"), strings.Split(colored, "\n")
	if len(plainLines) != len(colorLines) {
		t.Fatal("color changed line count")
	}
	for index := range plainLines {
		if terminal.VisibleWidth(colorLines[index]) != terminal.VisibleWidth(plainLines[index]) {
			t.Errorf("line %d width changed", index)
		}
	}
}

func TestTotalsAndProbablesPlainOutputMatchFrozenGoldens(t *testing.T) {
	team := domain.Team{ID: 147, Name: "New York Yankees", Location: "New York", ClubName: "Yankees", Abbreviation: "NYY", LeagueID: 103}
	yahoo := int64(14)
	available := int64(26)
	totals := RenderTotals([]domain.Standing{{Team: team, Wins: 70, Losses: 50, GamesBack: "-"}}, []domain.TeamTotals{{Team: team, Batting: domain.BattingStats{PlateAppearances: 4500, Runs: 600, HomeRuns: 180, RunsBattedIn: 575, StolenBases: 90, BattingAverage: .250, OnBasePercentage: .330, SluggingPercentage: .440, OnBasePlusSlugging: .770}, Pitching: domain.PitchingStats{Games: 120, GamesStarted: 120, InningsPitched: 1080, Wins: 70, Saves: 35, Holds: 60, Strikeouts: 1100, EarnedRunAverage: 3.75, WHIP: 1.20}, YahooPlayers: &yahoo, PlayersAvailable: &available}}, false, terminal.Plain)
	want, err := os.ReadFile("testdata/team-totals.txt")
	if err != nil {
		t.Fatal(err)
	}
	if totals != string(want) {
		t.Fatalf("totals differ\nGOT:\n%s\nWANT:\n%s", totals, want)
	}
	probability := .60
	slate := RenderSlate([]domain.SlateRow{{Date: "2026-08-15", GameID: 1, GameTime: "7:05 PM EDT", AwayTeam: "NYY", HomeTeam: "BOS", AwayPitcher: "Ace Starter", HomePitcher: "Home Pitcher", WinProbability: &probability, AwayFreeAgent: true, HomeMine: true}}, nil, terminal.Plain)
	want, err = os.ReadFile("testdata/probable-pitchers.txt")
	if err != nil {
		t.Fatal(err)
	}
	if slate != string(want) {
		t.Fatalf("slate differs\nGOT:\n%s\nWANT:\n%s", slate, want)
	}
}

func TestDisplayWarningsAndEmptySlateAreExplicit(t *testing.T) {
	if got := RenderSlate(nil, []string{"odds unavailable"}, terminal.Plain); got != "WARNING — odds unavailable\nNo MLB games are scheduled.\n" {
		t.Fatalf("empty slate=%q", got)
	}
	if got := RenderTotals(nil, nil, true, terminal.Plain); got != "STALE — showing the last complete MLB snapshot.\n" {
		t.Fatalf("stale totals=%q", got)
	}
}
