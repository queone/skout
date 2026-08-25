package display

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

const fantasyPlayerWidth = 26

// RenderFantasyPlayers renders a roster or a single-role player pool.
func RenderFantasyPlayers(title string, players []domain.StoredFantasyPlayer, mode terminal.ColorMode) string {
	roster := title != "HITTERS" && title != "PITCHERS"
	var output strings.Builder
	if roster {
		fmt.Fprintf(&output, "%s %s\n", terminal.TableHeading("ROSTER:", mode), terminal.Good(title, mode))
	}
	for _, section := range []struct{ role, heading string }{{"B", "HITTER"}, {"P", "PITCHER"}} {
		var rows []domain.StoredFantasyPlayer
		for _, player := range players {
			if player.Role == section.role {
				rows = append(rows, player)
			}
		}
		if len(rows) == 0 {
			continue
		}
		output.WriteString(terminal.TableHeading(fantasyPlayerHeader(roster, section.heading), mode))
		output.WriteByte('\n')
		for _, player := range rows {
			output.WriteString(fantasyPlayerRow(player, roster, mode))
			output.WriteByte('\n')
		}
		if roster {
			output.WriteString(terminal.Usage(fantasyTotalRow(section.role, rows), mode))
			output.WriteByte('\n')
		}
	}
	return output.String()
}

func fantasyPlayerHeader(roster bool, role string) string {
	if !roster && role == "HITTER" {
		return fmt.Sprintf("%-26s  %-5s  %-8s  %-1s  %4s  %4s  %6s  %5s  %5s  %5s  %5s  %5s  %5s  %5s  %5s  %5s  %3s  %3s  %4s  %4s  %5s  OWNER", role, "POS", "STATUS", "B", "YR", "ECR", "xwOBA", "EV", "BRL%", "HH%", "K%", "BB%", "SPD", "PA", "OBP", "OPS", "R", "HR", "RBI", "SB", "AVG")
	}
	if !roster {
		return fmt.Sprintf("%-26s  %-5s  %-8s  %-1s  %4s  %4s  %5s  %6s  %5s  %5s  %5s  %5s  %5s  %4s  %3s  %3s  %4s  %5s  %5s  OWNER", role, "POS", "STATUS", "T", "YR", "ECR", "FBV", "WHIFF%", "CH%", "GB%", "K%", "BB%", "IP", "QS", "W", "SV", "K", "ERA", "WHIP")
	}
	if role == "HITTER" {
		return fmt.Sprintf("SLOT  %-26s  %-5s  %-17s  %-1s  %4s  %6s  %5s  %3s  %3s  %4s  %5s  %5s", role, "POS", "STATUS", "B", "YR", "PA", "OBP", "R", "HR", "RBI", "SB", "AVG")
	}
	return fmt.Sprintf("SLOT  %-26s  %-5s  %-17s  %-1s  %4s  %6s  %5s  %3s  %3s  %4s  %5s  %5s", role, "POS", "STATUS", "T", "YR", "IP", "QS", "W", "SV", "K", "ERA", "WHIP")
}

func fantasyPlayerRow(player domain.StoredFantasyPlayer, roster bool, mode terminal.ColorMode) string {
	if !roster {
		return fantasyPoolRow(player, mode)
	}
	slot := "-"
	if player.Slot != nil {
		slot = *player.Slot
	}
	status := player.Status
	if status == "" {
		status = player.GameStatus
	}
	if status == "" {
		status = "NoGame"
	}
	status = fantasyIndicator(fantasyFit(status, 17), player.GameIndicator, mode)
	rank := "—"
	if player.Rank != nil {
		rank = strconv.FormatInt(*player.Rank, 10)
	}
	hand := player.Hand
	if hand == "" {
		hand = "-"
	}
	identity := fantasyFit(strings.TrimSpace(player.Name+" "+player.Team), fantasyPlayerWidth)
	prefix := fmt.Sprintf("%-4s  %s  %s  %s  %s  %4s  ", slot, identity, FantasyPositions(player.Positions, player.IsCloser), status, hand, rank)
	var stats string
	if player.Role == "B" {
		stats = fmt.Sprintf("%6.0f  %5s  %3.0f  %3.0f  %4.0f  %5.0f  %5s", player.Batting[0], fantasyRate(player.Batting[1], 3), player.Batting[2], player.Batting[3], player.Batting[4], player.Batting[5], fantasyRate(player.Batting[6], 3))
	} else {
		stats = fmt.Sprintf("%6.1f  %5.0f  %3.0f  %3.0f  %4.0f  %5.2f  %5.2f", player.Pitching[0], player.Pitching[1], player.Pitching[2], player.Pitching[3], player.Pitching[4], player.Pitching[5], player.Pitching[6])
	}
	row := prefix + stats
	if slot == "IL" || strings.HasPrefix(player.Status, "IL") {
		return terminal.Warning(row, mode)
	}
	if slot == "BN" {
		return terminal.Dim(row, mode)
	}
	return terminal.RosterRow(row, player.Status, mode)
}

func fantasyPoolRow(player domain.StoredFantasyPlayer, mode terminal.ColorMode) string {
	status := player.Status
	if status == "" || status == "A" {
		status = player.GameStatus
	}
	if status == "" {
		status = "NoGame"
	}
	status = fantasyIndicator(fantasyFit(status, 8), player.GameIndicator, mode)
	hand, rank, ecr := player.Hand, "—", "—"
	if hand == "" {
		hand = "-"
	}
	if player.Rank != nil {
		rank = strconv.FormatInt(*player.Rank, 10)
	}
	if player.ExpertConsensusRank != nil {
		ecr = strconv.FormatInt(*player.ExpertConsensusRank, 10)
	}
	owner := "<available>"
	if player.Owner != nil {
		owner = *player.Owner
	} else if player.YahooPlayerID == nil {
		owner = "<not yet in Yahoo>"
	}
	identity := fantasyFit(strings.TrimSpace(player.Name+" "+player.Team), fantasyPlayerWidth)
	if player.Role == "P" {
		advanced := []string{fantasyOptional(player.PitchingAdvanced[0], 5, 1, false), fantasyOptional(player.PitchingAdvanced[1], 6, 1, true), fantasyOptional(player.PitchingAdvanced[2], 5, 1, true), fantasyOptional(player.PitchingAdvanced[3], 5, 1, true), fantasyOptional(player.PitchingAdvanced[4], 5, 1, true), fantasyOptional(player.PitchingAdvanced[5], 5, 1, true)}
		return fmt.Sprintf("%s  %s  %s  %s  %4s  %4s  %s  %5.1f  %4.0f  %3.0f  %3.0f  %4.0f  %5.2f  %5.2f  %s", identity, FantasyPositions(player.Positions, player.IsCloser), status, hand, rank, ecr, strings.Join(advanced, "  "), player.Pitching[0], player.Pitching[1], player.Pitching[2], player.Pitching[3], player.Pitching[4], player.Pitching[5], player.Pitching[6], terminal.Dim(strings.TrimRight(fantasyFit(owner, 20), " "), mode))
	}
	advanced := []string{fantasyOptional(player.HittingAdvanced[0], 6, 3, false), fantasyOptional(player.HittingAdvanced[1], 5, 1, false), fantasyOptional(player.HittingAdvanced[2], 5, 1, true), fantasyOptional(player.HittingAdvanced[3], 5, 1, true), fantasyOptional(player.HittingAdvanced[4], 5, 1, true), fantasyOptional(player.HittingAdvanced[5], 5, 1, true), fantasyOptional(player.HittingAdvanced[6], 5, 1, false)}
	ops := fantasyOptional(player.HittingAdvanced[7], 5, 3, false)
	return fmt.Sprintf("%s  %s  %s  %s  %4s  %4s  %s  %5.0f  %5s  %5s  %3.0f  %3.0f  %4.0f  %4.0f  %5s  %s", identity, FantasyPositions(player.Positions, player.IsCloser), status, hand, rank, ecr, strings.Join(advanced, "  "), player.Batting[0], fantasyRate(player.Batting[1], 3), ops, player.Batting[2], player.Batting[3], player.Batting[4], player.Batting[5], fantasyRate(player.Batting[6], 3), terminal.Dim(strings.TrimRight(fantasyFit(owner, 20), " "), mode))
}

func fantasyTotalRow(role string, players []domain.StoredFantasyPlayer) string {
	var values [7]float64
	for _, player := range players {
		if role == "B" {
			values[0] += player.Batting[0]
			values[1] += player.Batting[1] * player.Batting[0]
			values[2] += player.Batting[2]
			values[3] += player.Batting[3]
			values[4] += player.Batting[4]
			values[5] += player.Batting[5]
			values[6] += player.Batting[6] * player.Batting[0]
		} else {
			values[0] += player.Pitching[0]
			for i := 1; i < 5; i++ {
				values[i] += player.Pitching[i]
			}
			values[5] += player.Pitching[5] * player.Pitching[0]
			values[6] += player.Pitching[6] * player.Pitching[0]
		}
	}
	if values[0] > 0 {
		if role == "B" {
			values[1] /= values[0]
			values[6] /= values[0]
		} else {
			values[5] /= values[0]
			values[6] /= values[0]
		}
	}
	prefix := fmt.Sprintf("%-4s  %-26s  %-5s  %-17s  %-1s  %4s  ", "", "TOTAL", "", "", "", "")
	if role == "B" {
		return prefix + fmt.Sprintf("%6.0f  %5s  %3.0f  %3.0f  %4.0f  %5.0f  %5s", values[0], fantasyRate(values[1], 3), values[2], values[3], values[4], values[5], fantasyRate(values[6], 3))
	}
	return prefix + fmt.Sprintf("%6.1f  %5.0f  %3.0f  %3.0f  %4.0f  %5.2f  %5.2f", values[0], values[1], values[2], values[3], values[4], values[5], values[6])
}

// RenderLeagueTotals renders every team in standing order with weighted rates.
func RenderLeagueTotals(teams []store.StoredFantasyTeam, players []domain.StoredFantasyPlayer, mode terminal.ColorMode) string {
	width := 4
	for _, team := range teams {
		width = max(width, utf8.RuneCountInString(team.Name))
	}
	header := fmt.Sprintf("%-*s  %4s  %9s  %5s  %5s  %5s  %4s  %5s  %5s  %3s  %3s  %3s  %3s  %5s  %6s  %3s  %3s  %4s  %5s  %5s", width, "TEAM", "RANK", "WLT", "PCT", "GB", "BDGT", "WVR", "MOVES", "PA", "R", "HR", "RBI", "SB", "AVG", "IP", "W", "SV", "K", "ERA", "WHIP")
	leaderWins, leaderLosses := int64(0), int64(0)
	for _, team := range teams {
		if team.Wins > leaderWins {
			leaderWins, leaderLosses = team.Wins, team.Losses
		}
	}
	var output strings.Builder
	output.WriteString(terminal.TableHeading(header, mode))
	output.WriteByte('\n')
	for _, team := range teams {
		var batting, pitching [7]float64
		for _, player := range players {
			if player.Owner == nil || *player.Owner != team.Name {
				continue
			}
			pa, ip := player.Batting[0], player.Pitching[0]
			batting[0] += pa
			batting[1] += player.Batting[1] * pa
			for index := 2; index <= 5; index++ {
				batting[index] += player.Batting[index]
			}
			batting[6] += player.Batting[6] * pa
			pitching[0] += ip
			for index := 1; index <= 4; index++ {
				pitching[index] += player.Pitching[index]
			}
			pitching[5] += player.Pitching[5] * ip
			pitching[6] += player.Pitching[6] * ip
		}
		if batting[0] > 0 {
			batting[1], batting[6] = batting[1]/batting[0], batting[6]/batting[0]
		}
		if pitching[0] > 0 {
			pitching[5], pitching[6] = pitching[5]/pitching[0], pitching[6]/pitching[0]
		}
		total := team.Wins + team.Losses + team.Ties
		pct, behind := "—", "—"
		if total > 0 {
			pct = fantasyRate((float64(team.Wins)+float64(team.Ties)/2)/float64(total), 3)
			value := float64(leaderWins-team.Wins+team.Losses-leaderLosses) / 2
			if value > 0 {
				behind = fmt.Sprintf("%.1f", value)
			}
		}
		wlt := "—"
		if total > 0 {
			wlt = fmt.Sprintf("%d-%d-%d", team.Wins, team.Losses, team.Ties)
		}
		rank := "—"
		if team.Rank > 0 {
			rank = strconv.FormatInt(team.Rank, 10)
		}
		fmt.Fprintf(&output, "%-*s  %4s  %9s  %5s  %5s  $%4d  %4d  %5d  %5.0f  %3.0f  %3.0f  %3.0f  %3.0f  %5s  %6.1f  %3.0f  %3.0f  %4.0f  %5.2f  %5.2f\n", width, team.Name, rank, wlt, pct, behind, team.FAABBalance, team.WaiverPriority, team.Moves, batting[0], batting[2], batting[3], batting[4], batting[5], fantasyRateOrDash(batting[6], 3), pitching[0], pitching[2], pitching[3], pitching[4], pitching[5], pitching[6])
	}
	return output.String()
}

// RenderWeeklyTotals renders league categories in their stored display order.
func RenderWeeklyTotals(title, period string, team domain.MatchupTeam, categories []store.StoredFantasyCategory, stale bool, mode terminal.ColorMode) string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s\n%s\n", terminal.TableHeading(title, mode), period)
	if stale {
		output.WriteString("STALE — showing the last complete Yahoo weekly snapshot.\n")
	}
	for _, category := range categories {
		if value, ok := team.Stats[strconv.FormatInt(category.StatID, 10)]; ok {
			fmt.Fprintf(&output, "%-6s %s\n", category.Abbreviation, value)
		}
	}
	return output.String()
}

// RenderPlayerDetail renders a stable hitter or pitcher detail card.
func RenderPlayerDetail(player domain.StoredFantasyPlayer, logs []domain.PlayerGameLog, average *domain.HitterAverage, nextProjection string, stale bool, today string, mode terminal.ColorMode) string {
	role := "HITTER"
	if player.Role == "P" {
		role = "PITCHER"
	}
	age, rank := "—", "—"
	if birth, err := time.Parse("2006-01-02", player.BirthDate); err == nil {
		if current, err := time.Parse("2006-01-02", today); err == nil && !current.Before(birth) {
			years := current.Year() - birth.Year()
			if current.YearDay() < birth.AddDate(years, 0, 0).YearDay() {
				years--
			}
			age = strconv.Itoa(years)
		}
	}
	if player.Rank != nil {
		rank = strconv.FormatInt(*player.Rank, 10)
	}
	hand := player.Hand
	if hand == "" {
		hand = "-"
	}
	status := player.Status
	if status == "" {
		status = "—"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "%s\n%-22s  %s  %-8s  %-1s  %3s  %4s\n\n", terminal.TableHeading(fmt.Sprintf("%-22s  POS    STATUS    B  AGE    YR", role), mode), strings.TrimSpace(player.Name+" "+player.Team), FantasyPositions(player.Positions, player.IsCloser), status, hand, age, rank)
	owner := "<available>"
	if player.Owner != nil {
		owner = *player.Owner
	}
	if player.Role == "P" {
		output.WriteString(terminal.TableHeading("SOURCE      FBV  WHIFF%    CH%    GB%     K%    BB%  OWNER", mode) + "\n")
		fmt.Fprintf(&output, "%-8s %s  %s  %s  %s  %s  %s  %s\n\n", "SAVANT", fantasyOptional(player.PitchingAdvanced[0], 5, 1, false), fantasyOptional(player.PitchingAdvanced[1], 6, 1, true), fantasyOptional(player.PitchingAdvanced[2], 5, 1, true), fantasyOptional(player.PitchingAdvanced[3], 5, 1, true), fantasyOptional(player.PitchingAdvanced[4], 5, 1, true), fantasyOptional(player.PitchingAdvanced[5], 5, 1, true), owner)
		output.WriteString(terminal.TableHeading("SPLIT         IP   QS    W   SV     K    ERA   WHIP", mode) + "\n")
		fmt.Fprintf(&output, "%-8s %7.1f %4.0f %4.0f %4.0f %5.0f %6.2f %6.2f\n", "CURRENT", player.Pitching[0], player.Pitching[1], player.Pitching[2], player.Pitching[3], player.Pitching[4], player.Pitching[5], player.Pitching[6])
	} else {
		output.WriteString(terminal.TableHeading("SOURCE    xwOBA     EV   BRL%    HH%     K%    BB%    SPD  OWNER", mode) + "\n")
		fmt.Fprintf(&output, "%-7s  %s  %s  %s  %s  %s  %s  %s  %s\n\n", "SAVANT", fantasyOptional(player.HittingAdvanced[0], 5, 3, false), fantasyOptional(player.HittingAdvanced[1], 5, 1, false), fantasyOptional(player.HittingAdvanced[2], 5, 1, true), fantasyOptional(player.HittingAdvanced[3], 5, 1, true), fantasyOptional(player.HittingAdvanced[4], 5, 1, true), fantasyOptional(player.HittingAdvanced[5], 5, 1, true), fantasyOptional(player.HittingAdvanced[6], 5, 1, false), owner)
		output.WriteString(terminal.TableHeading("SPLIT           PA     OBP    OPS     R    HR   RBI    SB    AVG", mode) + "\n")
		if average == nil {
			output.WriteString("AVG162G          —       —      —     —     —     —     —      —\n")
		} else {
			fmt.Fprintf(&output, "%-12s  %4d  %6s  %5s  %4d  %4d  %4d  %4d  %5s\n", "AVG162G", average.PlateAppearances, fantasyRate(average.OnBasePercentage, 3), fantasyRate(average.OnBasePlusSlugging, 3), average.Runs, average.HomeRuns, average.RunsBattedIn, average.StolenBases, fantasyRate(average.BattingAverage, 3))
		}
		fmt.Fprintf(&output, "%-12s  %4.0f  %6s  %5s  %4.0f  %4.0f  %4.0f  %4.0f  %5s\n", "CURRENT", player.Batting[0], fantasyRate(player.Batting[1], 3), fantasyOptional(player.HittingAdvanced[7], 0, 3, false), player.Batting[2], player.Batting[3], player.Batting[4], player.Batting[5], fantasyRate(player.Batting[6], 3))
	}
	if nextProjection != "" {
		output.WriteString(nextProjection + "\n")
	}
	if stale {
		output.WriteString("GAME LOG data may be stale — refresh unavailable.\n")
	}
	if player.Role == "P" {
		output.WriteString(terminal.TableHeading("GAME LOG   OPP      STATUS      IP    W   SV    K    ERA   WHIP", mode) + "\n")
	} else {
		output.WriteString(terminal.TableHeading("GAME LOG   OPP      RESULT    BO  H/AB     R    HR   RBI    SB    AVG", mode) + "\n")
	}
	for _, log := range logs {
		if player.Role == "P" {
			fmt.Fprintf(&output, "%-10s %-8s %-8s %5s %4s %4s %4s %6s %6s\n", fantasyDate(log.Date), log.Opponent, log.Status, logField(log.Line, "IP"), logField(log.Line, "W"), logField(log.Line, "SV"), logField(log.Line, "K"), logField(log.Line, "ERA"), logField(log.Line, "WHIP"))
		} else {
			hab := logField(log.Line, "H") + "/" + logField(log.Line, "AB")
			order := "—"
			if log.BattingOrder > 0 {
				order = strconv.Itoa(log.BattingOrder)
			}
			fmt.Fprintf(&output, "%-9s  %-7s  %-7s  %2s  %4s  %4s  %4s  %4s  %4s  %5s\n", fantasyDate(log.Date), log.Opponent, log.Status, order, hab, logField(log.Line, "R"), logField(log.Line, "HR"), logField(log.Line, "RBI"), logField(log.Line, "SB"), logField(log.Line, "AVG"))
		}
	}
	if strings.HasPrefix(player.Status, "IL") {
		output.WriteByte('\n')
		output.WriteString(terminal.TableHeading("INJURIES", mode) + "\n")
		if player.InjuryNote == "" {
			output.WriteString(player.Status + "\n")
		} else {
			fmt.Fprintf(&output, "%s: %s\n", player.Status, player.InjuryNote)
		}
	}
	return output.String()
}

// FantasyPositions compresses eligible positions to the frozen five-cell form.
func FantasyPositions(value string, closer bool) string {
	all := strings.FieldsFunc(value, func(r rune) bool { return r == ',' })
	positions := all[:0]
	for _, item := range all {
		item = strings.TrimSpace(item)
		if item != "" && !strings.EqualFold(item, "uti") && !strings.EqualFold(item, "util") && !strings.EqualFold(item, "p") {
			positions = append(positions, item)
		}
	}
	if len(positions) == 0 {
		positions = all
	}
	literal := strings.Join(positions, ",")
	if literal == "" && closer {
		literal = "RP"
	}
	if closer {
		literal += "1"
	}
	if utf8.RuneCountInString(literal) <= 5 {
		return fantasyFit(literal, 5)
	}
	if len(positions) >= 6 {
		if closer {
			return "All1 "
		}
		return "All  "
	}
	rank := map[string]int{"C": 0, "1B": 1, "2B": 2, "3B": 3, "SS": 4, "OF": 5, "SP": 6, "RP": 7}
	sort.SliceStable(positions, func(i, j int) bool { return rank[positions[i]] < rank[positions[j]] })
	letters := map[string]string{"C": "C", "1B": "1", "2B": "2", "3B": "3", "SS": "S", "OF": "O", "SP": "P", "RP": "R"}
	var compressed strings.Builder
	for _, position := range positions {
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
	return fantasyFit(compressed.String(), 5)
}

func fantasyIndicator(status string, indicator domain.GameIndicator, mode terminal.ColorMode) string {
	marker := ""
	switch indicator.Kind {
	case domain.GameIndicatorBattingOrder:
		marker = strconv.Itoa(indicator.Order)
	case domain.GameIndicatorStartingPitcher, domain.GameIndicatorOutOfLineup:
		marker = "●"
	}
	if marker == "" {
		return status
	}
	styled := terminal.Good(marker, mode)
	if indicator.Kind == domain.GameIndicatorOutOfLineup {
		styled = terminal.Injury(marker, mode)
	}
	return strings.Replace(status, " "+marker+" ", " "+styled+" ", 1)
}

func fantasyOptional(value *float64, width, precision int, percent bool) string {
	if value == nil {
		if width == 0 {
			return "—"
		}
		return fmt.Sprintf("%*s", width, "—")
	}
	formatted := fmt.Sprintf("%.*f", precision, *value)
	if precision == 3 {
		formatted = strings.TrimPrefix(formatted, "0")
	}
	if percent {
		formatted += "%"
	}
	if width == 0 {
		return formatted
	}
	return fmt.Sprintf("%*s", width, formatted)
}

func fantasyFit(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		runes = runes[:width]
	}
	return string(runes) + strings.Repeat(" ", max(0, width-len(runes)))
}

func fantasyRate(value float64, precision int) string {
	return strings.TrimPrefix(fmt.Sprintf("%.*f", precision, value), "0")
}

func fantasyRateOrDash(value float64, precision int) string {
	if value == 0 {
		return "—"
	}
	return fantasyRate(value, precision)
}

func fantasyDate(value string) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return parsed.Format("Jan 02")
}

func logField(line, name string) string {
	fields := strings.Fields(line)
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == name {
			return fields[index+1]
		}
	}
	return "-"
}
