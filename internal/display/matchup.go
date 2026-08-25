package display

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

// RenderMatchup renders two fantasy rosters and their category totals side by side.
func RenderMatchup(view domain.MatchupView, mode terminal.ColorMode) string {
	mine, opponent := &view.Matchup.Teams[0], &view.Matchup.Teams[1]
	if view.Matchup.Teams[1].TeamKey == view.Mine.TeamKey {
		mine, opponent = opponent, mine
	}
	var output strings.Builder
	fmt.Fprintf(&output, "%s %s\n", terminal.TableHeading("MATCHUP WEEK:", mode), terminal.Dim(fmt.Sprintf("%d of 26 (%s / %s)", view.Matchup.Week, matchupDate(view.Matchup.WeekStart), matchupDate(view.Matchup.WeekEnd)), mode))
	if view.Stale {
		output.WriteString(terminal.Warning("STALE — Yahoo unavailable; showing the last complete matchup snapshot", mode) + "\n")
	}
	fmt.Fprintf(&output, "%s           %s\n", matchupTeamDivider(*mine, view.Teams, mode), strings.TrimRight(matchupTeamDivider(*opponent, view.Teams, mode), " "))
	renderMatchupPlayers(&output, view.Mine.Players, view.Opponent.Players, "B", *mine, *opponent, mode)
	output.WriteByte('\n')
	renderMatchupPlayers(&output, view.Mine.Players, view.Opponent.Players, "P", *mine, *opponent, mode)
	output.WriteByte('\n')
	output.WriteString(terminal.TableHeading("SUMMARY", mode) + "\n")
	fmt.Fprintf(&output, "%s %s / %s / %s\n", terminal.TableHeading(fmt.Sprintf("%-12s", "W/T/L"), mode), terminal.Good(strconv.Itoa(mine.Wins), mode), terminal.Warning(strconv.Itoa(mine.Ties), mode), terminal.Injury(strconv.Itoa(mine.Losses), mode))
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
				fmt.Fprintf(&output, "%12s %s\n", "", odds.Line)
			} else {
				fmt.Fprintf(&output, "%s %s\n", terminal.TableHeading(fmt.Sprintf("%-12s", label), mode), odds.Line)
				emitted = true
			}
		}
	}
	return output.String()
}

// RenderLocalMatchup renders only locally known roster data after Yahoo failure.
func RenderLocalMatchup(view domain.LocalMatchupView, mode terminal.ColorMode) string {
	return terminal.Warning("YAHOO UNAVAILABLE — showing local roster; matchup totals and opponent unavailable", mode) + "\n" + RenderFantasyPlayers(domain.CleanFantasyTeamName(view.TeamName), view.Players, mode)
}

func matchupTeamDivider(matchup domain.MatchupTeam, teams []domain.FantasyTeam, mode terminal.ColorMode) string {
	wins, losses, ties, rank := int64(matchup.Wins), int64(matchup.Losses), int64(matchup.Ties), int64(0)
	for _, team := range teams {
		if team.TeamKey == matchup.TeamKey {
			wins, losses, ties, rank = team.Wins, team.Losses, team.Ties, team.Rank
			break
		}
	}
	rankText := "—"
	if rank > 0 {
		rankText = ordinal(rank)
	}
	played := matchup.CompletedGames + matchup.LiveGames
	total := played + matchup.RemainingGames
	value := fmt.Sprintf("%s %s", terminal.Good(domain.CleanFantasyTeamName(matchup.Name), mode), terminal.Dim(fmt.Sprintf("(%d-%d-%d | %s) - %d rem (%d/%d)", wins, losses, ties, rankText, matchup.RemainingGames, played, total), mode))
	if width := terminal.VisibleWidth(value); width < 67 {
		value += strings.Repeat(" ", 67-width)
	}
	return value
}

func renderMatchupPlayers(output *strings.Builder, leftInput, rightInput []domain.PlayerWeekStats, role string, mine, opponent domain.MatchupTeam, mode terminal.ColorMode) {
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
	fmt.Fprintf(output, "%s           %s\n", matchupTotals(role, mine, opponent, true, mode), matchupTotals(role, opponent, mine, false, mode))
}

func matchupPlayerCell(player domain.PlayerWeekStats, role string, mode terminal.ColorMode) string {
	status := player.InjuryStatus
	if status == "" {
		status = "NoGame"
	}
	status = fantasyIndicator(fantasyFit(status, 17), player.GameIndicator, mode)
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

func matchupTotals(role string, team, other domain.MatchupTeam, mine bool, mode terminal.ColorMode) string {
	categories := [][4]string{{"H/AB", "60", "6", "0"}, {"R", "7", "4", "0"}, {"HR", "12", "4", "0"}, {"RBI", "13", "4", "0"}, {"SB", "16", "5", "0"}, {"AVG", "3", "7", "0"}}
	if role == "P" {
		categories = [][4]string{{"IP", "50", "6", "0"}, {"W", "28", "4", "0"}, {"SV", "32", "4", "0"}, {"K", "42", "4", "0"}, {"ERA", "26", "6", "1"}, {"WHIP", "27", "6", "1"}}
	}
	var value strings.Builder
	value.WriteString(strings.Repeat(" ", 37))
	for index, category := range categories {
		current := matchupStat(team, category[0], category[1])
		width, _ := strconv.Atoi(category[2])
		padded := fmt.Sprintf("%*s", width, current)
		if index == 0 {
			value.WriteString(terminal.Dim(padded, mode))
			continue
		}
		otherValue := matchupStat(other, category[0], category[1])
		left, leftErr := strconv.ParseFloat(current, 64)
		right, rightErr := strconv.ParseFloat(otherValue, 64)
		if leftErr == nil && rightErr == nil && left != right {
			winning := left > right
			if category[3] == "1" {
				winning = left < right
			}
			if winning {
				padded = terminal.Good(padded, mode)
			} else {
				padded = terminal.Injury(padded, mode)
			}
		}
		value.WriteString(padded)
	}
	_ = mine
	return value.String()
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
