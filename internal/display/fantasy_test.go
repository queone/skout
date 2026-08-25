package display

import (
	"os"
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

func sampleFantasyPlayers() []domain.StoredFantasyPlayer {
	yahooHitter, yahooPitcher := int64(1), int64(2)
	rankHitter, rankPitcher := int64(5), int64(8)
	owner, of, sp := "Operators", "OF", "SP"
	xwoba, velo := .360, 96.5
	return []domain.StoredFantasyPlayer{
		{YahooPlayerID: &yahooHitter, Name: "Ada Hitter", Team: "NYY", Role: "B", Positions: "OF,Util", Hand: "R", Rank: &rankHitter, Owner: &owner, Slot: &of, Batting: [7]float64{100, .350, 20, 5, 18, 3, .280}, HittingAdvanced: [8]*float64{&xwoba}},
		{YahooPlayerID: &yahooPitcher, Name: "Ace Pitcher", Team: "BOS", Role: "P", Positions: "SP,P", Hand: "L", Rank: &rankPitcher, Owner: &owner, Slot: &sp, Pitching: [7]float64{60, 6, 5, 0, 70, 3.20, 1.10}, PitchingAdvanced: [6]*float64{&velo}},
	}
}

func TestFantasyDisplayGoldensAndSemanticWidths(t *testing.T) {
	players := sampleFantasyPlayers()
	teams := []store.StoredFantasyTeam{{TeamKey: "t.1", Name: "Operators", Rank: 1, Wins: 4, Losses: 2, FAABBalance: 65, WaiverPriority: 2, Moves: 7}}
	category := []store.StoredFantasyCategory{{StatID: 7, Abbreviation: "R", Sequence: 1}}
	team := domain.MatchupTeam{Name: "Operators", Stats: map[string]string{"7": "5"}}
	tests := []struct{ name, path, output string }{
		{"Roster", "testdata/fantasy/roster.txt", RenderFantasyPlayers("Operators", players, terminal.Plain)},
		{"LeagueTotals", "testdata/fantasy/league-totals.txt", RenderLeagueTotals(teams, players, terminal.Plain)},
		{"WeeklyTotals", "testdata/fantasy/weekly-totals.txt", RenderWeeklyTotals("Operators", "WEEK 7", team, category, false, terminal.Plain)},
		{"HitterPool", "testdata/fantasy/hitter-pool.txt", RenderFantasyPlayers("HITTERS", players[:1], terminal.Plain)},
		{"PitcherPool", "testdata/fantasy/pitcher-pool.txt", RenderFantasyPlayers("PITCHERS", players[1:], terminal.Plain)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { assertDisplayGolden(t, test.path, test.output) })
	}
	plain := RenderFantasyPlayers("Operators", players, terminal.Plain)
	colored := RenderFantasyPlayers("Operators", players, terminal.Color)
	if strings.Contains(strings.Split(plain, "\n")[1], "OWNER") || terminal.VisibleWidth(strings.Split(plain, "\n")[1]) != terminal.VisibleWidth(strings.Split(colored, "\n")[1]) {
		t.Fatalf("plain=%q colored=%q", plain, colored)
	}
	if FantasyPositions("C,1B,2B,3B,SS,OF,Util", false) != "All  " || FantasyPositions("SP,RP,P", true) != "PR1  " {
		t.Fatalf("position compression changed: %q %q", FantasyPositions("C,1B,2B,3B,SS,OF,Util", false), FantasyPositions("SP,RP,P", true))
	}
}

func TestFantasyDetailGoldensMissingValuesAndStaleLabels(t *testing.T) {
	players := sampleFantasyPlayers()
	logs := []domain.PlayerGameLog{{Date: "2026-08-24", GameID: 1, Opponent: "@BOS", BattingOrder: 4, Line: "H 2 AB 4 R 1 HR 1 RBI 2 SB 0 AVG .500"}}
	hitter := RenderPlayerDetail(players[0], logs, &domain.HitterAverage{PlateAppearances: 600, OnBasePercentage: .350, OnBasePlusSlugging: .800, Runs: 90, HomeRuns: 25, RunsBattedIn: 80, StolenBases: 10, BattingAverage: .275}, "NEXT20PA", true, "2026-08-25", terminal.Plain)
	pitcher := RenderPlayerDetail(players[1], []domain.PlayerGameLog{{Date: "2026-08-24", GameID: 2, Opponent: "vs TB", Line: "IP 6.0 W 1 SV 0 K 7 ERA 3.00 WHIP 1.00"}}, nil, "", false, "2026-08-25", terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/hitter-detail.txt", hitter)
	assertDisplayGolden(t, "testdata/fantasy/pitcher-detail.txt", pitcher)
	if !strings.Contains(hitter, "GAME LOG data may be stale") || !strings.Contains(hitter, "—") || strings.Contains(pitcher, "AVG162G") {
		t.Fatalf("hitter=%q pitcher=%q", hitter, pitcher)
	}
}

func assertDisplayGolden(t *testing.T, path, output string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if output != string(want) {
		t.Fatalf("%s differs\nGOT:\n%s\nWANT:\n%s", path, output, want)
	}
}
