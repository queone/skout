package display

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

// RenderMatchup renders the pairing's scoreboard, its odds lines, and both
// rosters side by side.
func RenderMatchup(view domain.MatchupView, mode terminal.ColorMode) string {
	var output strings.Builder
	detail := fmt.Sprintf("%d of 26 (%s / %s)", view.Matchup.Week, matchupDate(view.Matchup.WeekStart), matchupDate(view.Matchup.WeekEnd))
	if view.Day != "" {
		detail += "  ·  " + matchupDate(view.Day)
	}
	fmt.Fprintf(&output, "%s %s\n", terminal.TableHeading("MATCHUP WEEK:", mode), terminal.Dim(detail, mode))
	if view.Stale {
		output.WriteString(terminal.Warning("STALE — Yahoo unavailable; showing the last complete matchup snapshot", mode) + "\n")
	}
	output.WriteByte('\n')
	output.WriteString(leagueMatchupTable([]domain.Matchup{view.Matchup}, view.Teams, view.Mine.TeamKey, mode))
	for _, mineSide := range []bool{true, false} {
		label, emitted := "MY ODDS", false
		if !mineSide {
			label = "OPP ODDS"
		}
		for _, odds := range view.Odds {
			if odds.Mine != mineSide {
				continue
			}
			if emitted {
				fmt.Fprintf(&output, "%10s%s\n", "", odds.Line)
			} else {
				fmt.Fprintf(&output, "%s  %s\n", terminal.TableHeading(fmt.Sprintf("%-8s", label), mode), odds.Line)
				emitted = true
			}
		}
	}
	output.WriteByte('\n')
	renderMatchupPlayers(&output, view.Mine.Players, view.Opponent.Players, "B", mode)
	output.WriteByte('\n')
	renderMatchupPlayers(&output, view.Mine.Players, view.Opponent.Players, "P", mode)
	return output.String()
}

// RenderLocalMatchup renders only locally known roster data after Yahoo failure.
func RenderLocalMatchup(view domain.LocalMatchupView, mode terminal.ColorMode) string {
	return terminal.Warning("YAHOO UNAVAILABLE — showing local roster; matchup totals and opponent unavailable", mode) + "\n" + RenderFantasyPlayers(domain.CleanFantasyTeamName(view.TeamName), view.Players, mode)
}

func renderMatchupPlayers(output *strings.Builder, leftInput, rightInput []domain.PlayerWeekStats, role string, mode terminal.ColorMode) {
	var left, right []domain.PlayerWeekStats
	for _, player := range leftInput {
		if player.PositionType == role {
			left = append(left, player)
		}
	}
	for _, player := range rightInput {
		if player.PositionType == role {
			right = append(right, player)
		}
	}
	header := "HITTER              STATUS             H/AB   R  HR RBI   SB    AVG"
	if role == "P" {
		header = "PITCHER             STATUS                IP   W  SV   K   ERA  WHIP"
	}
	fmt.Fprintf(output, "%s    %s   %s\n", terminal.TableHeading(header, mode), terminal.Dim(fmt.Sprintf("%-4s", "SLOT"), mode), terminal.TableHeading(header, mode))
	for index := 0; index < max(len(left), len(right)); index++ {
		leftCell := strings.Repeat(" ", 67)
		rightCell, slot := "", ""
		if index < len(left) {
			leftCell = matchupPlayerCell(left[index], role, mode)
			slot = left[index].SlotPosition.String()
		}
		if index < len(right) {
			rightCell = matchupPlayerCell(right[index], role, mode)
		}
		fmt.Fprintf(output, "%s    %s   %s\n", leftCell, terminal.Dim(fmt.Sprintf("%-4s", slot), mode), rightCell)
	}
}

func matchupPlayerCell(player domain.PlayerWeekStats, role string, mode terminal.ColorMode) string {
	status := player.InjuryStatus
	if status == "" {
		status = "NoGame"
	}
	status = fantasyIndicator(fantasyFit(status, 17), player.GameIndicator, player.SlotPosition == domain.PositionBench, mode)
	name := matchupName(player)
	var stats string
	if role == "B" {
		hab, avg := player.HAB, player.BattingAverage
		if hab == "" {
			hab = "—"
		}
		if avg == "" {
			avg = "—"
		}
		stats = fmt.Sprintf("%6s%4d%4d%4d%5d%7s", hab, player.Runs, player.HomeRuns, player.RunsBattedIn, player.StolenBases, avg)
	} else {
		ip, era, whip := player.InningsPitched, player.EarnedRunAverage, player.WHIP
		if ip == "" {
			ip = "—"
		}
		if era == "" {
			era = "—"
		}
		if whip == "" {
			whip = "—"
		}
		stats = fmt.Sprintf("%6s%4d%4d%4d%6s%6s", ip, player.Wins, player.Saves, player.Strikeouts, era, whip)
	}
	row := name + status + stats
	if strings.EqualFold(strings.TrimSpace(player.InjuryStatus), "NA") {
		return terminal.Inactive(row, mode)
	}
	if player.SlotPosition == domain.PositionInjuredList || strings.HasPrefix(player.InjuryStatus, "IL") {
		return terminal.Warning(row, mode)
	}
	if player.SlotPosition == domain.PositionBench {
		return terminal.Dim(row, mode)
	}
	return row
}

func matchupName(player domain.PlayerWeekStats) string {
	name := player.Name
	if first, rest, found := strings.Cut(name, " "); found && first != "" {
		name = string([]rune(first)[0]) + " " + rest
	}
	if player.Team != "" {
		maxName := max(0, 20-len([]rune(player.Team))-3)
		name = string([]rune(name)[:min(len([]rune(name)), maxName)]) + " " + player.Team
	}
	return fantasyFit(name, 20)
}

// matchupCategory is one scoring column: its label, Yahoo stat id, cell width,
// and whether the lower value wins the category.
type matchupCategory struct {
	label, id string
	width     int
	lowWins   bool
}

// matchupCategories returns the hitting or pitching totals columns in display order.
func matchupCategories(role string) []matchupCategory {
	if role == "P" {
		return []matchupCategory{{"IP", "50", 6, false}, {"W", "28", 4, false}, {"SV", "32", 4, false}, {"K", "42", 4, false}, {"ERA", "26", 6, true}, {"WHIP", "27", 6, true}}
	}
	return []matchupCategory{{"H/AB", "60", 6, false}, {"R", "7", 4, false}, {"HR", "12", 4, false}, {"RBI", "13", 4, false}, {"SB", "16", 5, false}, {"AVG", "3", 7, false}}
}

// matchupCategoryCell pads one category value to its column width and colors it
// green when team wins the category against other and red when it loses.
func matchupCategoryCell(team, other domain.MatchupTeam, category matchupCategory, mode terminal.ColorMode) string {
	current := matchupStat(team, category.label, category.id)
	padded := fmt.Sprintf("%*s", category.width, current)
	left, leftErr := strconv.ParseFloat(current, 64)
	right, rightErr := strconv.ParseFloat(matchupStat(other, category.label, category.id), 64)
	if leftErr != nil || rightErr != nil || left == right {
		return padded
	}
	if (left > right) != category.lowWins {
		return terminal.Good(padded, mode)
	}
	return terminal.Injury(padded, mode)
}

func matchupStat(team domain.MatchupTeam, name, id string) string {
	if value := team.Stats[name]; value != "" {
		return value
	}
	if value := team.Stats[id]; value != "" {
		return value
	}
	return "—"
}

func matchupDate(value string) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	formatted := parsed.Format("Mon Jan-02")
	return formatted[:4] + strings.ToLower(formatted[4:7]) + formatted[7:]
}

func ordinal(value int64) string {
	suffix := "th"
	if value%100 < 11 || value%100 > 13 {
		switch value % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", value, suffix)
}

// RenderLeagueMatchups renders every matchup for one week as stacked team
// pairs of category totals under one shared header.
func RenderLeagueMatchups(view domain.LeagueMatchupsView, mode terminal.ColorMode) string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s %s\n", terminal.TableHeading("MATCHUPS WEEK:", mode), terminal.Dim(fmt.Sprintf("%d of 26 (%s / %s)", view.Week, matchupDate(view.WeekStart), matchupDate(view.WeekEnd)), mode))
	if view.Stale {
		output.WriteString(terminal.Warning("STALE — Yahoo unavailable; showing the last complete scoreboard snapshot", mode) + "\n")
	}
	output.WriteByte('\n')
	output.WriteString(leagueMatchupTable(view.Matchups, view.Teams, view.TeamKey, mode))
	return output.String()
}

// leagueMatchupTable renders the shared scoreboard: one column header, then
// each matchup as two stacked team rows, with the saved team's pairing first.
func leagueMatchupTable(input []domain.Matchup, teams []domain.FantasyTeam, teamKey string, mode terminal.ColorMode) string {
	width := 4
	for _, matchup := range input {
		for _, team := range matchup.Teams {
			width = max(width, utf8.RuneCountInString(domain.CleanFantasyTeamName(team.Name)))
		}
	}
	columns := leagueMatchupColumns()
	var header strings.Builder
	fmt.Fprintf(&header, "%-*s  %4s  %-10s  %-6s", width, "TEAM", "RANK", "REC", "SCORE")
	for _, column := range columns {
		fmt.Fprintf(&header, "%*s", column.width, column.label)
	}
	var output strings.Builder
	output.WriteString(terminal.TableHeading(header.String(), mode) + "\n")
	matchups := make([]domain.Matchup, 0, len(input))
	for _, matchup := range input {
		if matchup.Teams[0].TeamKey == teamKey || matchup.Teams[1].TeamKey == teamKey {
			matchups = append([]domain.Matchup{matchup}, matchups...)
			continue
		}
		matchups = append(matchups, matchup)
	}
	for index, matchup := range matchups {
		if index > 0 {
			output.WriteByte('\n')
		}
		for side, team := range matchup.Teams {
			output.WriteString(leagueMatchupRow(team, matchup.Teams[1-side], teams, teamKey, width, columns, mode) + "\n")
		}
	}
	return output.String()
}

// leagueMatchupColumns returns the ten scoring categories with league-view widths.
func leagueMatchupColumns() []matchupCategory {
	widths := map[string]int{"R": 4, "HR": 4, "RBI": 4, "SB": 4, "AVG": 6, "W": 5, "SV": 4, "K": 4, "ERA": 6, "WHIP": 6}
	var columns []matchupCategory
	for _, role := range []string{"B", "P"} {
		for _, category := range matchupCategories(role)[1:] {
			category.width = widths[category.label]
			columns = append(columns, category)
		}
	}
	return columns
}

// leagueMatchupRow renders one team's line: name, dim rank and record, the
// colored running score, and colored category cells against its opponent.
func leagueMatchupRow(team, other domain.MatchupTeam, teams []domain.FantasyTeam, teamKey string, width int, columns []matchupCategory, mode terminal.ColorMode) string {
	name := fmt.Sprintf("%-*s", width, domain.CleanFantasyTeamName(team.Name))
	if team.TeamKey == teamKey {
		name = terminal.Good(name, mode)
	}
	rank, record := "—", "—"
	for _, stored := range teams {
		if stored.TeamKey != team.TeamKey {
			continue
		}
		if stored.Rank > 0 {
			rank = ordinal(stored.Rank)
		}
		record = fmt.Sprintf("%d-%d-%d", stored.Wins, stored.Losses, stored.Ties)
		break
	}
	score := fmt.Sprintf("%-6s", fmt.Sprintf("%d-%d-%d", team.Wins, team.Ties, team.Losses))
	switch {
	case team.Wins > team.Losses:
		score = terminal.Good(score, mode)
	case team.Wins < team.Losses:
		score = terminal.Injury(score, mode)
	default:
		score = terminal.Warning(score, mode)
	}
	var row strings.Builder
	row.WriteString(fmt.Sprintf("%s  %s  %s  %s", name, terminal.Dim(fmt.Sprintf("%4s", rank), mode), terminal.Dim(fmt.Sprintf("%-10s", record), mode), score))
	for _, column := range columns {
		row.WriteString(matchupCategoryCell(team, other, column, mode))
	}
	return row.String()
}
