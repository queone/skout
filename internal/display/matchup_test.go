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
