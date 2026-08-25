// Package display renders deterministic provider-neutral command output.
package display

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/terminal"
)

// RosterGroup pairs one display heading with its complete roster.
type RosterGroup struct {
	Heading string
	Players []domain.RosterPlayer
}

// RenderRosters renders grouped 40-man rosters.
func RenderRosters(groups []RosterGroup, warnings []string, mode terminal.ColorMode) string {
	var output strings.Builder
	for _, note := range warnings {
		message := note
		if !strings.HasPrefix(note, "OWNER data") {
			message = "WARNING — " + note
		}
		output.WriteString(terminal.Warning(message, mode))
		output.WriteByte('\n')
	}
	for groupIndex, group := range groups {
		if groupIndex > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(terminal.TableHeading(group.Heading, mode))
		output.WriteByte('\n')
		twoWay := twoWayIDs(group.Players)
		roles := []struct{ role, heading, headers string }{
			{"H", "HITTER", "POS    STATUS             B    YR     PA    OBP    R   HR  RBI   SB    AVG  OWNER"},
			{"P", "PITCHER", "POS    STATUS             T    YR     IP     QS    W   SV    K    ERA   WHIP  OWNER"},
		}
		for _, role := range roles {
			var players []domain.RosterPlayer
			for _, player := range group.Players {
				if player.PrimaryType == role.role {
					players = append(players, player)
				}
			}
			if len(players) == 0 && role.role == "P" {
				continue
			}
			output.WriteString(terminal.TableHeading(fmt.Sprintf("%-26s  %s", role.heading, role.headers), mode))
			output.WriteByte('\n')
			for _, player := range players {
				qualifier := ""
				if twoWay[player.MLBAMID] {
					if role.role == "H" {
						qualifier = " (Hitter)"
					} else {
						qualifier = " (Pitcher)"
					}
				}
				pool := ""
				if !player.InYahooPool {
					pool = " †"
				}
				identity := fit(fmt.Sprintf("%s %s%s%s", player.Name, player.TeamAbbreviation, qualifier, pool), 26)
				positionValue := player.Position
				if player.EligiblePositions != "" {
					positionValue = displayPositions(player.EligiblePositions, player.IsCloser)
				}
				position := fit(positionValue, 5)
				statusValue := statusLabel(player.Status)
				if unavailable(player.InjuryStatus) {
					statusValue = statusWithInjury(player.GameStatus, player.InjuryStatus)
				} else if player.GameStatus != "" {
					statusValue = player.GameStatus
				}
				status := fit(statusValue, 17)
				hand := player.BatSide
				if role.role == "P" {
					hand = player.PitchHand
				}
				if hand == "" {
					hand = "—"
				}
				yearRank := "—"
				if player.YahooRank != nil {
					yearRank = fmt.Sprintf("%d", *player.YahooRank)
				}
				owner := terminal.Dim("<not yet in Yahoo>", mode)
				if player.Owner != nil {
					owner = terminal.Dim(clip(*player.Owner, 20), mode)
				} else if player.InYahooPool {
					owner = terminal.Good("<available>", mode)
				}
				var stats string
				if role.role == "H" {
					stats = fmt.Sprintf("%5d  %5s  %3d  %3d  %3d  %3d  %5s", player.PlateAppearances, rate(player.OnBasePercentage, 3), player.Runs, player.HomeRuns, player.RunsBattedIn, player.StolenBases, rate(player.BattingAverage, 3))
				} else {
					stats = fmt.Sprintf("%5s  %5d  %3d  %3d  %4d  %5.2f  %5.2f", baseballInnings(player.InningsPitched), player.QualityStarts, player.Wins, player.Saves, player.Strikeouts, player.EarnedRunAverage, player.WHIP)
				}
				row := fmt.Sprintf("%s  %s  %s  %-1s  %4s  %s  %s\n", identity, position, status, hand, yearRank, stats, owner)
				output.WriteString(terminal.RosterRow(row, player.Status, mode))
			}
		}
	}
	return output.String()
}

// RenderTotals renders league and division standings with inline totals.
func RenderTotals(standings []domain.Standing, totals []domain.TeamTotals, stale bool, mode terminal.ColorMode) string {
	var output strings.Builder
	if stale {
		output.WriteString(terminal.Warning("STALE — showing the last complete MLB snapshot.", mode))
		output.WriteByte('\n')
	}
	for _, league := range []struct {
		id    int64
		label string
	}{{103, "American League (AL)"}, {104, "National League (NL)"}} {
		var leagueRows []domain.Standing
		for _, row := range standings {
			if row.Team.LeagueID == league.id {
				leagueRows = append(leagueRows, row)
			}
		}
		if len(leagueRows) == 0 {
			continue
		}
		if output.Len() > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(terminal.TableHeading(league.label, mode))
		output.WriteByte('\n')
		output.WriteString(terminal.TableHeading("TEAM    W    L    PCT     GB   YP     PA    OBP    R   HR  RBI   SB    AVG      IP   QS    W   SV     K    ERA   WHIP", mode))
		output.WriteByte('\n')
		for _, division := range []string{"East", "Central", "West"} {
			var rows []domain.Standing
			for _, row := range leagueRows {
				if divisionFor(row.Team.ID) == division {
					rows = append(rows, row)
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Wins != rows[j].Wins {
					return rows[i].Wins > rows[j].Wins
				}
				if rows[i].Losses != rows[j].Losses {
					return rows[i].Losses < rows[j].Losses
				}
				return rows[i].Team.Abbreviation < rows[j].Team.Abbreviation
			})
			if len(rows) == 0 {
				continue
			}
			output.WriteString(terminal.TableHeading(division, mode))
			output.WriteByte('\n')
			for _, row := range rows {
				var total *domain.TeamTotals
				for index := range totals {
					if totals[index].Team.ID == row.Team.ID {
						total = &totals[index]
						break
					}
				}
				games := row.Wins + row.Losses
				pct := "—"
				if games > 0 {
					pct = rate(float64(row.Wins)/float64(games), 3)
				}
				yp, pa, obp, runs, homeRuns, rbi, stolenBases, average := "—", "—", "—", "—", "—", "—", "—", "—"
				innings, qualityStarts, wins, saves, strikeouts, era, whip := "—", "—", "—", "—", "—", "—", "—"
				if total != nil {
					if total.YahooPlayers != nil {
						yp = fmt.Sprintf("%d", *total.YahooPlayers)
					}
					pa = fmt.Sprintf("%d", total.Batting.PlateAppearances)
					obp = rateOrDash(total.Batting.OnBasePercentage, 3)
					runs = fmt.Sprintf("%d", total.Batting.Runs)
					homeRuns = fmt.Sprintf("%d", total.Batting.HomeRuns)
					rbi = fmt.Sprintf("%d", total.Batting.RunsBattedIn)
					stolenBases = fmt.Sprintf("%d", total.Batting.StolenBases)
					average = rateOrDash(total.Batting.BattingAverage, 3)
					innings = fmt.Sprintf("%.1f", total.Pitching.InningsPitched)
					qualityStarts = fmt.Sprintf("%d", total.Pitching.QualityStarts)
					wins = fmt.Sprintf("%d", total.Pitching.Wins)
					saves = fmt.Sprintf("%d", total.Pitching.Saves)
					strikeouts = fmt.Sprintf("%d", total.Pitching.Strikeouts)
					era = fmt.Sprintf("%.2f", total.Pitching.EarnedRunAverage)
					whip = fmt.Sprintf("%.2f", total.Pitching.WHIP)
				}
				contextColumns := []string{terminal.Dim(fmt.Sprintf("%3d", row.Wins), mode), terminal.Dim(fmt.Sprintf("%3d", row.Losses), mode), terminal.Dim(fmt.Sprintf("%5s", pct), mode), terminal.Dim(fmt.Sprintf("%5s", gamesBack(row.GamesBack)), mode), terminal.Dim(fmt.Sprintf("%3s", yp), mode), terminal.Dim(fmt.Sprintf("%5s", pa), mode), terminal.Dim(fmt.Sprintf("%5s", obp), mode)}
				pitchingContext := []string{terminal.Dim(fmt.Sprintf("%6s", innings), mode), terminal.Dim(fmt.Sprintf("%3s", qualityStarts), mode)}
				fmt.Fprintf(&output, "%-4s  %s  %s  %s  %s  %s  %s  %s  %3s  %3s  %3s  %3s  %5s  %s  %s  %3s  %3s  %4s  %5s  %5s\n", row.Team.Abbreviation, contextColumns[0], contextColumns[1], contextColumns[2], contextColumns[3], contextColumns[4], contextColumns[5], contextColumns[6], runs, homeRuns, rbi, stolenBases, average, pitchingContext[0], pitchingContext[1], wins, saves, strikeouts, era, whip)
			}
		}
	}
	return output.String()
}

// RenderSlate renders one row per probable-pitcher game.
func RenderSlate(rows []domain.SlateRow, warnings []string, mode terminal.ColorMode) string {
	var output strings.Builder
	for _, warning := range warnings {
		output.WriteString(terminal.Dim("WARNING — "+warning, mode))
		output.WriteByte('\n')
	}
	date := ""
	for _, row := range rows {
		if row.Date != date {
			if date != "" {
				output.WriteByte('\n')
			}
			date = row.Date
			output.WriteString(terminal.TableHeading(date, mode))
			output.WriteByte('\n')
		}
		var percentage *int
		if row.WinProbability != nil {
			value := int(math.Round(*row.WinProbability * 100))
			percentage = &value
		}
		awayFavored := percentage != nil && *percentage > 50
		homeFavored := percentage != nil && *percentage < 50
		away := pitcherCell(row.AwayPitcher, row.AwayFreeAgent && awayFavored, row.AwayMine, mode)
		home := pitcherCell(row.HomePitcher, row.HomeFreeAgent && homeFavored, row.HomeMine, mode)
		filled, probability := 0, "—%"
		if percentage != nil {
			filled = (max(0, min(100, *percentage)) + 5) / 10
			probability = fmt.Sprintf("%d%%", *percentage)
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
		fmt.Fprintf(&output, "%s v %s  %-6s %-7s  %s %s\n", away, home, row.GameTime, row.AwayTeam+"@"+row.HomeTeam, bar, probability)
	}
	if len(rows) == 0 {
		output.WriteString("No MLB games are scheduled.\n")
	}
	return output.String()
}

func twoWayIDs(players []domain.RosterPlayer) map[int64]bool {
	roles := map[int64]map[string]bool{}
	for _, player := range players {
		if roles[player.MLBAMID] == nil {
			roles[player.MLBAMID] = map[string]bool{}
		}
		roles[player.MLBAMID][player.PrimaryType] = true
	}
	result := map[int64]bool{}
	for id, values := range roles {
		result[id] = len(values) > 1
	}
	return result
}
func statusWithInjury(gameStatus, injuryStatus string) string {
	for _, marker := range []string{" @ ", " v "} {
		if index := strings.LastIndex(gameStatus, marker); index >= 0 {
			return gameStatus[:index] + marker + injuryStatus
		}
	}
	return injuryStatus
}
func pitcherCell(name string, freeAgent, mine bool, mode terminal.ColorMode) string {
	fields := strings.Fields(name)
	last := "TBD"
	if len(fields) > 0 {
		last = fields[len(fields)-1]
	}
	suffix := ""
	if freeAgent {
		suffix = " (FA)"
	}
	value := fit(last+suffix, 16)
	if mine {
		return terminal.Warning(value, mode)
	}
	if freeAgent {
		return terminal.Good(value, mode)
	}
	if last == "TBD" {
		return terminal.Dim(value, mode)
	}
	return value
}
func divisionFor(id int64) string {
	switch id {
	case 110, 111, 139, 141, 147, 120, 121, 143, 144, 146:
		return "East"
	case 114, 116, 118, 142, 145, 112, 113, 134, 138, 158:
		return "Central"
	default:
		return "West"
	}
}
func statusLabel(status string) string {
	if status == "A" {
		return "Active"
	}
	if strings.HasPrefix(status, "D") {
		return "IL " + status
	}
	if status == "MIN" || status == "RM" {
		return "Minors"
	}
	if status == "" {
		return "—"
	}
	return status
}
func unavailable(status string) bool {
	upper := strings.ToUpper(status)
	return upper == "NA" || strings.HasPrefix(upper, "IL") || strings.HasPrefix(upper, "DL")
}
func baseballInnings(value float64) string {
	whole := int64(math.Floor(value))
	fraction := value - float64(whole)
	outs := 0
	if math.Abs(fraction-.1) < .01 {
		outs = 1
	} else if math.Abs(fraction-.2) < .01 {
		outs = 2
	} else if fraction < .17 {
		outs = 0
	} else if fraction < .5 {
		outs = 1
	} else {
		outs = 2
	}
	return fmt.Sprintf("%d.%d", whole, outs)
}
func clip(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		runes = runes[:width]
	}
	return string(runes)
}
func fit(value string, width int) string {
	value = clip(value, width)
	return value + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(value)))
}
func rate(value float64, precision int) string {
	return strings.TrimPrefix(fmt.Sprintf("%.*f", precision, value), "0")
}
func rateOrDash(value float64, precision int) string {
	if value == 0 {
		return "—"
	}
	return rate(value, precision)
}
func gamesBack(value string) string {
	if value == "" || value == "—" || value == "-" {
		return "--"
	}
	return value
}
func displayPositions(value string, closer bool) string {
	all := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '/' })
	filtered := make([]string, 0, len(all))
	for index := range all {
		all[index] = strings.TrimSpace(all[index])
		folded := strings.ToLower(all[index])
		if all[index] != "" && folded != "uti" && folded != "util" && folded != "p" {
			filtered = append(filtered, all[index])
		}
	}
	if len(filtered) == 0 {
		filtered = all
	}
	literal := strings.Join(filtered, ",")
	if literal == "" && closer {
		literal = "RP"
	}
	if closer {
		literal += "1"
	}
	if utf8.RuneCountInString(literal) <= 5 {
		return literal
	}
	if len(filtered) >= 6 {
		if closer {
			return "All1"
		}
		return "All"
	}
	ranks := map[string]int{"C": 0, "1B": 1, "2B": 2, "3B": 3, "SS": 4, "OF": 5, "SP": 6, "RP": 7}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, leftKnown := ranks[filtered[i]]
		right, rightKnown := ranks[filtered[j]]
		if !leftKnown {
			left = 99
		}
		if !rightKnown {
			right = 99
		}
		return left < right
	})
	letters := map[string]string{"C": "C", "1B": "1", "2B": "2", "3B": "3", "SS": "S", "OF": "O", "SP": "P", "RP": "R"}
	var compressed strings.Builder
	for _, position := range filtered {
		letter := letters[position]
		if letter == "" && position != "" {
			letter = string([]rune(position)[0])
		}
		compressed.WriteString(letter)
		if closer && position == "RP" {
			compressed.WriteByte('1')
		}
		if utf8.RuneCountInString(compressed.String()) >= 5 {
			break
		}
	}
	if closer && !strings.Contains(compressed.String(), "1") && utf8.RuneCountInString(compressed.String()) < 5 {
		compressed.WriteByte('1')
	}
	return compressed.String()
}
