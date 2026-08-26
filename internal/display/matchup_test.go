package display

import (
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

func sampleMatchupView() domain.MatchupView {
	matchup := domain.Matchup{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{{TeamKey: "t.1", Name: "💎 Operators", Stats: map[string]string{"7": "5", "3": ".300"}, Wins: 2, Ties: 1}, {TeamKey: "t.2", Name: "Rivals", Stats: map[string]string{"7": "3", "3": ".250"}, Losses: 2, Ties: 1}}}
	return domain.MatchupView{Matchup: matchup, Mine: domain.RosterWeekStats{TeamKey: "t.1", TeamName: "Operators", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 1, Name: "Ada Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionOutfield, HAB: "2/4", Runs: 1, BattingAverage: ".500"}}}, Opponent: domain.RosterWeekStats{TeamKey: "t.2", TeamName: "Rivals", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 2, Name: "Grace Hitter", Team: "BOS", PositionType: "B", SlotPosition: domain.PositionOutfield, HAB: "1/4", BattingAverage: ".250"}}}, Teams: []domain.FantasyTeam{{TeamKey: "t.1", Name: "Operators", Rank: 1, Wins: 4, Losses: 2}, {TeamKey: "t.2", Name: "Rivals", Rank: 2, Wins: 2, Losses: 4}}, Odds: []domain.MatchupOdds{{Mine: true, Line: "Ace v Rival  NYY@BOS  ██████░░░░ 60%"}}}
}

func TestMatchupGoldensStaleAndColorAlignment(t *testing.T) {
	view := sampleMatchupView()
	daily := RenderMatchup(view, terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/matchup-daily.txt", daily)
	view.Stale = true
	weekly := RenderMatchup(view, terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/matchup-weekly.txt", weekly)
	local := RenderLocalMatchup(domain.LocalMatchupView{TeamName: "💎 Operators"}, terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/matchup-local.txt", local)
	colored := RenderMatchup(view, terminal.Color)
	plainLines, coloredLines := strings.Split(weekly, "\n"), strings.Split(colored, "\n")
	if !strings.Contains(weekly, "STALE") || strings.Contains(weekly, "💎") || len(plainLines) != len(coloredLines) {
		t.Fatalf("weekly=%q colored=%q", weekly, colored)
	}
	for index := range plainLines {
		if terminal.VisibleWidth(plainLines[index]) != terminal.VisibleWidth(coloredLines[index]) {
			t.Fatalf("line %d width plain=%d color=%d", index, terminal.VisibleWidth(plainLines[index]), terminal.VisibleWidth(coloredLines[index]))
		}
	}
}

func TestMatchupLineupIndicatorsKeepTheRustSubduedBoundary(t *testing.T) {
	base := domain.PlayerWeekStats{Name: "Ada Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionOutfield}
	bench := base
	bench.SlotPosition, bench.InjuryStatus = domain.PositionBench, "7:05p ● v BOS"
	bench.GameIndicator = domain.GameIndicator{Kind: domain.GameIndicatorOutOfLineup}
	injuredSlot := base
	injuredSlot.SlotPosition, injuredSlot.InjuryStatus = domain.PositionInjuredList, "7:05p 2 v BOS"
	injuredSlot.GameIndicator = domain.GameIndicator{Kind: domain.GameIndicatorBattingOrder, Order: 2}
	injuredStatus := base
	injuredStatus.InjuryStatus = "IL10 ● v BOS"
	injuredStatus.GameIndicator = domain.GameIndicator{Kind: domain.GameIndicatorOutOfLineup}

	tests := []struct {
		name   string
		player domain.PlayerWeekStats
		want   string
	}{
		{name: "Bench", player: bench, want: "\x1b[38;5;124m●\x1b[38;5;245m"},
		{name: "InjuredListSlot", player: injuredSlot, want: "\x1b[38;5;46m2\x1b[0m"},
		{name: "InjuredListStatus", player: injuredStatus, want: "\x1b[38;5;196m●\x1b[0m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plain := matchupPlayerCell(test.player, "B", terminal.Plain)
			colored := matchupPlayerCell(test.player, "B", terminal.Color)
			if !strings.Contains(colored, test.want) {
				t.Errorf("colored cell missing %q: %q", test.want, colored)
			}
			if terminal.VisibleWidth(plain) != terminal.VisibleWidth(colored) {
				t.Errorf("width differs: plain=%q colored=%q", plain, colored)
			}
		})
	}
}
