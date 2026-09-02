package display

import (
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

func sampleMatchupView() domain.MatchupView {
	matchup := domain.Matchup{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{{TeamKey: "t.1", Name: "💎 Operators", Stats: map[string]string{"7": "5", "3": ".300"}, Wins: 2, Ties: 1}, {TeamKey: "t.2", Name: "Rivals", Stats: map[string]string{"7": "3", "3": ".250"}, Losses: 2, Ties: 1}}}
	return domain.MatchupView{Matchup: matchup, Mine: domain.RosterWeekStats{TeamKey: "t.1", TeamName: "Operators", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 1, Name: "Ada Hitter", Team: "NYY", PositionType: "B", SlotPosition: domain.PositionOutfield, HAB: "2/4", Runs: 1, BattingAverage: ".500"}}}, Opponent: domain.RosterWeekStats{TeamKey: "t.2", TeamName: "Rivals", Week: 7, Players: []domain.PlayerWeekStats{{YahooPlayerID: 2, Name: "Grace Hitter", Team: "BOS", PositionType: "B", SlotPosition: domain.PositionOutfield, HAB: "1/4", BattingAverage: ".250"}}}, Teams: []domain.FantasyTeam{{TeamKey: "t.1", Name: "Operators", Rank: 1, Wins: 4, Losses: 2}, {TeamKey: "t.2", Name: "Rivals", Rank: 2, Wins: 2, Losses: 4}}, Day: "2026-08-24", Odds: []domain.MatchupOdds{{Mine: true, Line: "Ace v Rival  NYY@BOS  ██████░░░░ 60%"}}}
}

func TestMatchupGoldensStaleAndColorAlignment(t *testing.T) {
	view := sampleMatchupView()
	daily := RenderMatchup(view, terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/matchup-daily.txt", daily)
	view.Stale, view.Day = true, ""
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

func TestMatchupCellColorsInactiveRowsDarkRed(t *testing.T) {
	base := domain.PlayerWeekStats{Name: "Ada Hitter", Team: "NYY", PositionType: "B"}
	inactive := base
	inactive.SlotPosition, inactive.InjuryStatus = domain.PositionThirdBase, "NA"
	inactiveBench := base
	inactiveBench.SlotPosition, inactiveBench.InjuryStatus = domain.PositionBench, "NA"
	injured := base
	injured.SlotPosition, injured.InjuryStatus = domain.PositionOutfield, "IL15"
	for name, test := range map[string]struct {
		player domain.PlayerWeekStats
		prefix string
	}{
		"InactiveActiveSlot": {inactive, "\x1b[38;5;88m"},
		"InactiveBench":      {inactiveBench, "\x1b[38;5;88m"},
		"InjuredActiveSlot":  {injured, "\x1b[38;5;100m"},
	} {
		t.Run(name, func(t *testing.T) {
			plain := matchupPlayerCell(test.player, "B", terminal.Plain)
			colored := matchupPlayerCell(test.player, "B", terminal.Color)
			if !strings.HasPrefix(colored, test.prefix) || !strings.HasSuffix(colored, "\x1b[0m") || !strings.Contains(plain, test.player.InjuryStatus) {
				t.Fatalf("plain=%q colored=%q", plain, colored)
			}
			if terminal.VisibleWidth(plain) != terminal.VisibleWidth(colored) {
				t.Fatalf("width differs: plain=%q colored=%q", plain, colored)
			}
		})
	}
}

func sampleLeagueMatchupsView() domain.LeagueMatchupsView {
	return domain.LeagueMatchupsView{
		Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", TeamKey: "t.1",
		Matchups: []domain.Matchup{
			{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{
				{TeamKey: "t.3", Name: "Sluggers", Wins: 5, Losses: 5, Stats: map[string]string{"7": "30", "12": "7", "13": "24", "16": "4", "3": "0.289", "28": "4", "32": "3", "42": "56", "26": "3.60", "27": "1.23"}},
				{TeamKey: "t.4", Name: "Closers", Wins: 5, Losses: 5, Stats: map[string]string{"7": "27", "12": "9", "13": "36", "16": "5", "3": ".272", "28": "4", "32": "0", "42": "50", "26": "4.15"}},
			}},
			{Week: 7, WeekStart: "2026-08-24", WeekEnd: "2026-08-30", Teams: [2]domain.MatchupTeam{
				{TeamKey: "t.1", Name: "💎 Operators", Wins: 7, Ties: 2, Losses: 1, Stats: map[string]string{"7": "29", "12": "4", "13": "25", "16": "5", "3": ".237", "28": "2", "32": "1", "42": "65", "26": "2.87", "27": "1.24"}},
				{TeamKey: "t.2", Name: "Rivals", Wins: 1, Ties: 2, Losses: 7, Stats: map[string]string{"7": "19", "12": "4", "13": "19", "16": "2", "3": ".235", "28": "4", "32": "1", "42": "25", "26": "4.39", "27": "1.69"}},
			}},
		},
		Teams: []domain.FantasyTeam{{TeamKey: "t.1", Name: "Operators", Rank: 1, Wins: 4, Losses: 2}, {TeamKey: "t.2", Name: "Rivals", Rank: 2, Wins: 2, Losses: 4}, {TeamKey: "t.3", Name: "Sluggers", Rank: 3, Wins: 3, Losses: 3, Ties: 1}},
	}
}

func TestLeagueMatchupsGoldenListsSavedTeamFirstWithStaleLine(t *testing.T) {
	view := sampleLeagueMatchupsView()
	plain := RenderLeagueMatchups(view, terminal.Plain)
	assertDisplayGolden(t, "testdata/fantasy/league-matchups.txt", plain)
	if strings.Contains(plain, "💎") || strings.Index(plain, "Operators") > strings.Index(plain, "Sluggers") {
		t.Fatalf("plain=%q", plain)
	}
	view.Stale = true
	stale := RenderLeagueMatchups(view, terminal.Plain)
	lines := strings.Split(stale, "\n")
	if lines[1] != "STALE — Yahoo unavailable; showing the last complete scoreboard snapshot" || lines[2] != "" {
		t.Fatalf("stale=%q", stale)
	}
}

func TestLeagueMatchupsColorsWinnersLosersTiesAndScoresWithStableWidths(t *testing.T) {
	view := sampleLeagueMatchupsView()
	plain := RenderLeagueMatchups(view, terminal.Plain)
	colored := RenderLeagueMatchups(view, terminal.Color)
	plainLines, coloredLines := strings.Split(plain, "\n"), strings.Split(colored, "\n")
	if len(plainLines) != len(coloredLines) {
		t.Fatalf("plain=%q colored=%q", plain, colored)
	}
	for index := range plainLines {
		if terminal.VisibleWidth(plainLines[index]) != terminal.VisibleWidth(coloredLines[index]) {
			t.Fatalf("line %d width plain=%d color=%d", index, terminal.VisibleWidth(plainLines[index]), terminal.VisibleWidth(coloredLines[index]))
		}
	}
	operators, rivals, sluggers, closers := coloredLines[3], coloredLines[4], coloredLines[6], coloredLines[7]
	for name, test := range map[string]struct{ line, want string }{
		"SavedTeamNameGreen":     {operators, terminal.Good("Operators", terminal.Color)},
		"LeadingScoreGreen":      {operators, terminal.Good("7-2-1 ", terminal.Color)},
		"TrailingScoreRed":       {rivals, terminal.Injury("1-2-7 ", terminal.Color)},
		"TiedScoreYellow":        {sluggers, terminal.Warning("5-0-5 ", terminal.Color)},
		"DimRank":                {operators, terminal.Dim(" 1st", terminal.Color)},
		"DimMissingRecord":       {closers, terminal.Dim("—         ", terminal.Color)},
		"RunsWinnerGreen":        {operators, terminal.Good("  29", terminal.Color)},
		"RunsLoserRed":           {rivals, terminal.Injury("  19", terminal.Color)},
		"LowERAWinsGreen":        {operators, terminal.Good("  2.87", terminal.Color)},
		"HighERALosesRed":        {rivals, terminal.Injury("  4.39", terminal.Color)},
		"LowWHIPWinsGreen":       {operators, terminal.Good("  1.24", terminal.Color)},
		"HighWHIPLosesRed":       {rivals, terminal.Injury("  1.69", terminal.Color)},
		"TiedHomeRunsPlain":      {operators, terminal.Good("  29", terminal.Color) + "   4" + terminal.Good("  25", terminal.Color)},
		"MissingWHIPPlain":       {closers, terminal.Injury("  4.15", terminal.Color) + "     —"},
		"OpponentOfMissingPlain": {sluggers, terminal.Good("  3.60", terminal.Color) + "  1.23"},
	} {
		if !strings.Contains(test.line, test.want) {
			t.Errorf("%s: %q lacks %q", name, test.line, test.want)
		}
	}
	if strings.Contains(operators, terminal.Good("   4", terminal.Color)) || strings.Contains(rivals, terminal.Injury("   4", terminal.Color)) {
		t.Fatalf("tied category colored: operators=%q rivals=%q", operators, rivals)
	}
}

func TestMatchupLeadsWithScoreboardOddsAndDayLabel(t *testing.T) {
	view := sampleMatchupView()
	lines := strings.Split(RenderMatchup(view, terminal.Plain), "\n")
	if !strings.HasSuffix(lines[0], "  ·  Mon aug-24") || lines[1] != "" || !strings.HasPrefix(lines[2], "TEAM ") || !strings.HasPrefix(lines[3], "Operators ") || !strings.HasPrefix(lines[4], "Rivals ") || !strings.HasPrefix(lines[5], "MY ODDS   Ace v Rival") || lines[6] != "" || !strings.HasPrefix(lines[7], "HITTER ") {
		t.Fatalf("daily=%q", lines)
	}
	daily := strings.Join(lines, "\n")
	for _, removed := range []string{"SUMMARY", "W/T/L", "rem (", "—   5   —"} {
		if strings.Contains(daily, removed) {
			t.Fatalf("daily still contains %q: %q", removed, daily)
		}
	}
	view.Day, view.Stale = "", true
	weekly := strings.Split(RenderMatchup(view, terminal.Plain), "\n")
	if strings.Contains(weekly[0], "·") || !strings.HasPrefix(weekly[1], "STALE") || weekly[2] != "" {
		t.Fatalf("weekly=%q", weekly)
	}
	view.Stale = false
	colored := strings.Split(RenderMatchup(view, terminal.Color), "\n")
	league := strings.Split(RenderLeagueMatchups(domain.LeagueMatchupsView{Week: 7, WeekStart: view.Matchup.WeekStart, WeekEnd: view.Matchup.WeekEnd, Matchups: []domain.Matchup{view.Matchup}, Teams: view.Teams, TeamKey: "t.1"}, terminal.Color), "\n")
	if colored[2] != league[2] || colored[3] != league[3] || colored[4] != league[4] {
		t.Fatalf("scoreboard differs\nmatchup=%q\nleague=%q", colored[2:5], league[2:5])
	}
}
