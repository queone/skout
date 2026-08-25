package providers

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/queone/skout/internal/domain"
)

const yahooMaxPages = 20

// YahooFantasyErrorKind classifies a Yahoo fantasy boundary failure.
type YahooFantasyErrorKind string

const (
	YahooProviderError       YahooFantasyErrorKind = "provider"
	YahooInvalidInputError   YahooFantasyErrorKind = "invalid_input"
	YahooInvalidPayloadError YahooFantasyErrorKind = "invalid_payload"
	YahooIncompleteError     YahooFantasyErrorKind = "incomplete"
)

// YahooFantasyError is one bounded, credential-free Yahoo failure.
type YahooFantasyError struct {
	Kind   YahooFantasyErrorKind
	Detail string
}

func (failure *YahooFantasyError) Error() string {
	switch failure.Kind {
	case YahooProviderError:
		return "acquire Yahoo fantasy data: " + failure.Detail
	case YahooInvalidInputError:
		return "construct Yahoo fantasy request: " + failure.Detail + "; correct the value and retry"
	case YahooInvalidPayloadError:
		return "parse Yahoo fantasy response: " + failure.Detail + "; retry after Yahoo returns a valid response"
	default:
		return "validate Yahoo fantasy snapshot: " + failure.Detail + "; prior complete data was retained"
	}
}

func yahooFantasyError(kind YahooFantasyErrorKind, detail string) error {
	return &YahooFantasyError{Kind: kind, Detail: detail}
}

// StatCategory is one normalized Yahoo scoring category.
type StatCategory struct {
	StatID       int64  `json:"stat_id"`
	Abbreviation string `json:"abbreviation"`
	Name         string `json:"name"`
	SortOrder    int    `json:"sort_order"`
	DisplayOnly  bool   `json:"display_only"`
	Sequence     int64  `json:"sequence"`
}

// RosterPosition is one league position and its configured count.
type RosterPosition struct {
	Position domain.Position `json:"position"`
	Count    int64           `json:"count"`
}

// LeagueSettings is one complete league metadata and scoring payload.
type LeagueSettings struct {
	League          domain.League    `json:"league"`
	CurrentWeek     *int             `json:"current_week"`
	Categories      []StatCategory   `json:"categories"`
	RosterPositions []RosterPosition `json:"roster_positions"`
}

// LeagueRosters is one complete normalized league-roster response.
type LeagueRosters struct {
	Players []domain.FantasyPlayer     `json:"players"`
	Slots   []domain.FantasyRosterSlot `json:"slots"`
}

// YahooFantasySource is the public Yahoo boundary consumed by sync.
type YahooFantasySource interface {
	LeagueSettings(leagueKey string) (LeagueSettings, error)
	Standings(leagueKey string) ([]domain.FantasyTeam, error)
	LeagueRosters(leagueKey string, teamKeys []string) (LeagueRosters, error)
	FreeAgents(leagueKey string) ([]domain.FantasyPlayer, error)
	Scoreboard(leagueKey string, week *int) ([]domain.Matchup, error)
	RosterWeekStats(teamKey string, week int) (domain.RosterWeekStats, error)
}

func decodeYahoo(payload []byte) (any, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, yahooFantasyError(YahooInvalidPayloadError, "response is not valid JSON")
	}
	return parsedYahooRoot(value), nil
}

func parsedYahooRoot(value any) any {
	if object, ok := value.(map[string]any); ok {
		if root, exists := object["fantasy_content"]; exists {
			return root
		}
		if root, exists := object["data"]; exists {
			return root
		}
	}
	return value
}

func sortedYahooKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftErr := strconv.Atoi(keys[i])
		right, rightErr := strconv.Atoi(keys[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return keys[i] < keys[j]
	})
	return keys
}

func flattenedYahoo(value any) map[string]any {
	output := map[string]any{}
	flattenYahooInto(value, output)
	return output
}

func flattenYahooInto(value any, output map[string]any) {
	switch value := value.(type) {
	case map[string]any:
		for _, key := range sortedYahooKeys(value) {
			child := value[key]
			if _, numeric := strconv.Atoi(key); numeric != nil {
				if _, exists := output[key]; !exists {
					output[key] = child
				}
			}
			if key == "is_keeper" || key == "eligible_positions" || key == "eligible_positions_to_add" {
				continue
			}
			switch child.(type) {
			case []any, map[string]any:
				flattenYahooInto(child, output)
			}
		}
	case []any:
		for _, child := range value {
			flattenYahooInto(child, output)
		}
	}
}

func looksLikeYahooEntityFields(values []any) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		switch value := value.(type) {
		case map[string]any:
		case []any:
			if len(value) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func yahooEntityMaps(value any, identity string) []map[string]any {
	output := []map[string]any{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case []any:
			if len(value) > 0 {
				if fields, ok := value[0].([]any); ok && looksLikeYahooEntityFields(fields) {
					merged := flattenedYahoo(value)
					if _, found := merged[identity]; found {
						output = append(output, merged)
						return
					}
				}
			}
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			if _, found := value[identity]; found {
				output = append(output, flattenedYahoo(value))
				return
			}
			for _, key := range sortedYahooKeys(value) {
				visit(value[key])
			}
		}
	}
	visit(value)
	return output
}

func yahooText(values map[string]any, key string) string {
	switch value := values[key].(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		if value == math.Trunc(value) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func yahooInt(values map[string]any, key string) int64 {
	value := yahooText(values, key)
	integer, _ := strconv.ParseInt(value, 10, 64)
	return integer
}

func yahooDecimal(values map[string]any, key string) *float64 {
	value, ok := values[key]
	if !ok {
		return nil
	}
	switch value := value.(type) {
	case float64:
		return &value
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func yahooStringValues(value any, field string) []string {
	var output []string
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			output = append(output, value)
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			if child, found := value[field]; found {
				visit(child)
				return
			}
			for _, key := range sortedYahooKeys(value) {
				visit(value[key])
			}
		}
	}
	visit(value)
	return output
}

func yahooRank(values map[string]any) *int64 {
	overall := int64(0)
	seasons := map[int]int64{}
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				collect(child)
			}
		case map[string]any:
			if rankValue, found := value["player_rank"]; found {
				rank := flattenedYahoo(rankValue)
				value := yahooInt(rank, "rank_value")
				if value <= 0 {
					return
				}
				switch yahooText(rank, "rank_type") {
				case "OR":
					overall = value
				case "S":
					season := int(yahooInt(rank, "rank_season"))
					if season > 0 {
						seasons[season] = value
					}
				}
				return
			}
			for _, key := range sortedYahooKeys(value) {
				collect(value[key])
			}
		}
	}
	if ranks, found := values["player_ranks"]; found {
		collect(ranks)
	}
	years := make([]int, 0, len(seasons))
	for year := range seasons {
		years = append(years, year)
	}
	sort.Ints(years)
	current, previous := int64(0), int64(0)
	if len(years) > 0 {
		current = seasons[years[len(years)-1]]
	}
	if len(years) > 1 {
		previous = seasons[years[len(years)-2]]
	}
	selected := int64(0)
	switch {
	case current > 0 && overall > 0 && current != overall:
		selected = current
	case previous > 0:
		selected = previous
	case overall > 0:
		selected = overall
	case current > 0:
		selected = current
	default:
		selected = yahooInt(values, "rank_value")
	}
	if selected <= 0 {
		return nil
	}
	return &selected
}

// ParseLeagueSettings normalizes one complete Yahoo settings response.
func ParseLeagueSettings(leagueKey string, payload []byte) (LeagueSettings, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return LeagueSettings{}, err
	}
	maps := yahooEntityMaps(root, "league_key")
	metadata := flattenedYahoo(root)
	if len(maps) > 0 {
		metadata = maps[0]
	}
	name := yahooText(metadata, "name")
	season := int(yahooInt(metadata, "season"))
	numTeams := int(yahooInt(metadata, "num_teams"))
	if name == "" || season <= 0 || numTeams <= 0 {
		return LeagueSettings{}, yahooFantasyError(YahooIncompleteError, "league metadata is incomplete")
	}
	categories := []StatCategory{}
	for sequence, values := range yahooEntityMaps(root, "stat_id") {
		statID := yahooInt(values, "stat_id")
		if statID <= 0 {
			continue
		}
		sortOrder := 1
		if yahooText(values, "sort_order") == "0" {
			sortOrder = 0
		}
		categories = append(categories, StatCategory{StatID: statID, Abbreviation: yahooText(values, "abbr"), Name: yahooText(values, "name"), SortOrder: sortOrder, DisplayOnly: yahooText(values, "is_only_display_stat") == "1", Sequence: int64(sequence)})
	}
	positions := []RosterPosition{}
	for _, values := range yahooEntityMaps(root, "position") {
		position, count := yahooText(values, "position"), yahooInt(values, "count")
		if position != "" && count > 0 {
			positions = append(positions, RosterPosition{Position: domain.ParsePosition(position), Count: count})
		}
	}
	if len(categories) == 0 || len(positions) == 0 {
		return LeagueSettings{}, yahooFantasyError(YahooIncompleteError, "scoring categories or roster positions are missing")
	}
	league := domain.League{LeagueKey: leagueKey, Name: name, Season: season, NumTeams: numTeams, ScoringType: domain.ParseScoringType(yahooText(metadata, "scoring_type"))}
	for _, position := range positions {
		league.RosterPositions = append(league.RosterPositions, position.Position)
	}
	for _, category := range categories {
		if category.StatID < 50 {
			league.BattingCategories = append(league.BattingCategories, category.Abbreviation)
		} else {
			league.PitchingCategories = append(league.PitchingCategories, category.Abbreviation)
		}
	}
	var currentWeek *int
	if week := int(yahooInt(metadata, "current_week")); week > 0 {
		currentWeek = &week
	}
	return LeagueSettings{League: league, CurrentWeek: currentWeek, Categories: categories, RosterPositions: positions}, nil
}

// ParseStandings normalizes one complete Yahoo standings response.
func ParseStandings(leagueKey string, payload []byte) ([]domain.FantasyTeam, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return nil, err
	}
	unique := map[string]domain.FantasyTeam{}
	for _, values := range yahooEntityMaps(root, "team_key") {
		key := yahooText(values, "team_key")
		if key == "" {
			continue
		}
		moves := max(yahooInt(values, "number_of_moves"), yahooInt(values, "moves"))
		if _, found := unique[key]; !found {
			unique[key] = domain.FantasyTeam{TeamKey: key, LeagueKey: leagueKey, TeamID: yahooInt(values, "team_id"), Name: domain.CleanFantasyTeamName(yahooText(values, "name")), ManagerName: yahooText(values, "nickname"), IsOwnedByCurrentLogin: yahooText(values, "is_owned_by_current_login") == "1", WaiverPriority: yahooInt(values, "waiver_priority"), FAABBalance: yahooInt(values, "faab_balance"), Wins: yahooInt(values, "wins"), Losses: yahooInt(values, "losses"), Ties: yahooInt(values, "ties"), Moves: moves, Rank: yahooInt(values, "rank")}
		}
	}
	if len(unique) == 0 {
		return nil, yahooFantasyError(YahooIncompleteError, "standings contain no teams")
	}
	output := make([]domain.FantasyTeam, 0, len(unique))
	for _, team := range unique {
		output = append(output, team)
	}
	domain.SortFantasyTeams(output)
	return output, nil
}

func yahooEligiblePositions(values map[string]any) []domain.Position {
	positions := []domain.Position{}
	for _, label := range yahooStringValues(values["eligible_positions"], "position") {
		positions = append(positions, domain.ParsePosition(label))
	}
	return positions
}

func yahooPlayer(values map[string]any) domain.FantasyPlayer {
	return domain.FantasyPlayer{YahooPlayerID: yahooInt(values, "player_id"), Name: yahooText(values, "full"), MLBTeam: yahooText(values, "editorial_team_abbr"), DisplayPosition: yahooText(values, "display_position"), PositionType: yahooText(values, "position_type"), EligiblePositions: yahooEligiblePositions(values), InjuryStatus: yahooText(values, "status"), PercentOwned: yahooDecimal(values, "value"), PercentageStarted: yahooNestedDecimal(values, "percent_started"), YahooRank: yahooRank(values)}
}

func yahooNestedDecimal(values map[string]any, key string) *float64 {
	child, found := values[key]
	if !found {
		return nil
	}
	return yahooDecimal(flattenedYahoo(child), "value")
}

func parseLeagueRosterRoot(root any) (LeagueRosters, error) {
	players := map[int64]domain.FantasyPlayer{}
	slotSet := map[string]domain.FantasyRosterSlot{}
	for _, team := range yahooEntityMaps(root, "team_key") {
		teamKey := yahooText(team, "team_key")
		if teamKey == "" {
			continue
		}
		source := team["players"]
		for _, values := range yahooEntityMaps(source, "player_id") {
			playerID, selected := yahooInt(values, "player_id"), yahooText(values, "position")
			if playerID <= 0 || selected == "--" || selected == "" {
				continue
			}
			if _, found := players[playerID]; !found {
				players[playerID] = yahooPlayer(values)
			}
			slot := domain.FantasyRosterSlot{TeamKey: teamKey, YahooPlayerID: playerID, SlotPosition: domain.ParsePosition(selected)}
			slotSet[fmt.Sprintf("%s\x00%020d\x00%s", teamKey, playerID, selected)] = slot
		}
	}
	if len(players) == 0 || len(slotSet) == 0 {
		return LeagueRosters{}, yahooFantasyError(YahooIncompleteError, "complete league roster contains no players or slots")
	}
	result := LeagueRosters{}
	for _, player := range players {
		result.Players = append(result.Players, player)
	}
	for _, slot := range slotSet {
		result.Slots = append(result.Slots, slot)
	}
	domain.SortFantasyPlayers(result.Players)
	domain.SortFantasyRosterSlots(result.Slots)
	return result, nil
}

// ParseLeagueRosters normalizes one complete multi-team Yahoo roster response.
func ParseLeagueRosters(_ string, payload []byte) (LeagueRosters, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return LeagueRosters{}, err
	}
	return parseLeagueRosterRoot(root)
}

// ParseTeamRosters merges exact per-team responses and injects omitted echoed keys.
func ParseTeamRosters(teamKeys []string, payloads [][]byte) (LeagueRosters, error) {
	if len(teamKeys) == 0 || len(teamKeys) != len(payloads) {
		return LeagueRosters{}, yahooFantasyError(YahooInvalidInputError, "team roster keys and payloads must be non-empty and aligned")
	}
	wrapped := make([]any, 0, len(teamKeys))
	for index, payload := range payloads {
		root, err := decodeYahoo(payload)
		if err != nil {
			return LeagueRosters{}, err
		}
		team := flattenedYahoo(root)
		for _, candidate := range yahooEntityMaps(root, "team_key") {
			if yahooText(candidate, "team_key") == teamKeys[index] {
				team = candidate
				break
			}
		}
		team["team_key"] = teamKeys[index]
		wrapped = append(wrapped, team)
	}
	return parseLeagueRosterRoot(wrapped)
}

// ParseFreeAgents normalizes one Yahoo available-player page.
func ParseFreeAgents(payload []byte) ([]domain.FantasyPlayer, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return nil, err
	}
	unique := map[int64]domain.FantasyPlayer{}
	for _, values := range yahooEntityMaps(root, "player_id") {
		player := yahooPlayer(values)
		if player.YahooPlayerID > 0 {
			unique[player.YahooPlayerID] = player
		}
	}
	output := make([]domain.FantasyPlayer, 0, len(unique))
	for _, player := range unique {
		output = append(output, player)
	}
	domain.SortFantasyPlayers(output)
	return output, nil
}

func yahooTeamStatistics(team map[string]any) map[string]string {
	output := map[string]string{}
	source := team["team_stats"]
	if source == nil {
		source = team["stats"]
	}
	for _, stat := range yahooEntityMaps(source, "stat_id") {
		id, value := yahooText(stat, "stat_id"), yahooText(stat, "value")
		if id != "" && value != "" {
			output[id] = value
		}
	}
	return output
}

func yahooScoreboardTeamMaps(value any) []map[string]any {
	output := []map[string]any{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			if team, found := value["team"]; found {
				merged := flattenedYahoo(team)
				if yahooText(merged, "team_key") != "" {
					output = append(output, merged)
					return
				}
			}
			for _, key := range sortedYahooKeys(value) {
				visit(value[key])
			}
		}
	}
	visit(value)
	if len(output) == 0 {
		return yahooEntityMaps(value, "team_key")
	}
	return output
}

func yahooScoreboardMatchupMaps(value any) []map[string]any {
	output := []map[string]any{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				visit(child)
			}
		case map[string]any:
			_, week := value["week"]
			_, start := value["week_start"]
			_, end := value["week_end"]
			if week && start && end {
				output = append(output, flattenedYahoo(value))
			}
			for _, key := range sortedYahooKeys(value) {
				visit(value[key])
			}
		}
	}
	visit(value)
	return output
}

func yahooMatchupRecord(mine, opponent domain.MatchupTeam) (int, int, int) {
	categories := []struct {
		id        string
		lowerWins bool
	}{{"7", false}, {"12", false}, {"13", false}, {"16", false}, {"3", false}, {"28", false}, {"32", false}, {"42", false}, {"26", true}, {"27", true}}
	wins, losses, ties := 0, 0, 0
	for _, category := range categories {
		left, leftErr := strconv.ParseFloat(mine.Stats[category.id], 64)
		right, rightErr := strconv.ParseFloat(opponent.Stats[category.id], 64)
		if leftErr != nil || rightErr != nil || left == right {
			ties++
		} else if (left > right) != category.lowerWins {
			wins++
		} else {
			losses++
		}
	}
	return wins, losses, ties
}

// ParseScoreboard normalizes Yahoo weekly matchup shapes.
func ParseScoreboard(payload []byte) ([]domain.Matchup, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return nil, err
	}
	output := []domain.Matchup{}
	for _, values := range yahooScoreboardMatchupMaps(root) {
		week := int(yahooInt(values, "week"))
		teams := yahooScoreboardTeamMaps(values["teams"])
		if week <= 0 || len(teams) != 2 {
			continue
		}
		pair := [2]domain.MatchupTeam{}
		for index, team := range teams {
			pair[index] = domain.MatchupTeam{TeamKey: yahooText(team, "team_key"), TeamID: yahooInt(team, "team_id"), Name: domain.CleanFantasyTeamName(yahooText(team, "name")), IsCurrentLogin: yahooText(team, "is_owned_by_current_login") == "1", Stats: yahooTeamStatistics(team), Wins: int(yahooInt(team, "wins")), Losses: int(yahooInt(team, "losses")), Ties: int(yahooInt(team, "ties")), CompletedGames: int(yahooInt(team, "completed_games")), LiveGames: int(yahooInt(team, "live_games")), RemainingGames: int(yahooInt(team, "remaining_games"))}
		}
		if pair[0].Wins == 0 && pair[0].Losses == 0 && pair[0].Ties == 0 && pair[1].Wins == 0 && pair[1].Losses == 0 && pair[1].Ties == 0 {
			pair[0].Wins, pair[0].Losses, pair[0].Ties = yahooMatchupRecord(pair[0], pair[1])
			pair[1].Wins, pair[1].Losses, pair[1].Ties = pair[0].Losses, pair[0].Wins, pair[0].Ties
		}
		output = append(output, domain.Matchup{Week: week, WeekStart: yahooText(values, "week_start"), WeekEnd: yahooText(values, "week_end"), Status: yahooText(values, "status"), Teams: pair})
	}
	sort.SliceStable(output, func(i, j int) bool {
		if output[i].Week != output[j].Week {
			return output[i].Week < output[j].Week
		}
		return output[i].Teams[0].TeamKey < output[j].Teams[0].TeamKey
	})
	return output, nil
}

func yahooStat(stats map[string]string, values map[string]any, id, label string) string {
	if value := stats[id]; value != "" {
		return value
	}
	return yahooText(values, label)
}

func yahooStatInt(stats map[string]string, values map[string]any, id, label string) int {
	integer, _ := strconv.Atoi(yahooStat(stats, values, id, label))
	return integer
}

// ParseRosterWeekStats normalizes weekly or daily Yahoo roster statistics.
func ParseRosterWeekStats(teamKey string, week int, payload []byte) (domain.RosterWeekStats, error) {
	root, err := decodeYahoo(payload)
	if err != nil {
		return domain.RosterWeekStats{}, err
	}
	team := flattenedYahoo(root)
	for _, candidate := range yahooEntityMaps(root, "team_key") {
		if yahooText(candidate, "team_key") == teamKey {
			team = candidate
			break
		}
	}
	source := team["players"]
	if source == nil {
		source = root
	}
	players := []domain.PlayerWeekStats{}
	for _, values := range yahooEntityMaps(source, "player_id") {
		id := yahooInt(values, "player_id")
		if id <= 0 {
			continue
		}
		stats := yahooTeamStatistics(values)
		display, role := yahooText(values, "display_position"), yahooText(values, "position_type")
		if role == "" {
			for _, position := range strings.Split(display, ",") {
				if position = strings.TrimSpace(position); position == "P" || position == "SP" || position == "RP" {
					role = "P"
					break
				}
			}
			if role == "" && display != "" {
				role = "B"
			}
		}
		hab := yahooText(values, "H/AB")
		if _, hits := stats["8"]; hits {
			hab = fmt.Sprintf("%s-%s", yahooStat(stats, values, "8", "H"), yahooStat(stats, values, "6", "AB"))
		} else if _, atBats := stats["6"]; atBats {
			hab = fmt.Sprintf("%s-%s", yahooStat(stats, values, "8", "H"), yahooStat(stats, values, "6", "AB"))
		}
		players = append(players, domain.PlayerWeekStats{YahooPlayerID: id, Name: yahooText(values, "full"), Team: yahooText(values, "editorial_team_abbr"), PositionType: role, SlotPosition: domain.ParsePosition(yahooText(values, "position")), InjuryStatus: yahooText(values, "status"), HAB: hab, Runs: yahooStatInt(stats, values, "7", "R"), HomeRuns: yahooStatInt(stats, values, "12", "HR"), RunsBattedIn: yahooStatInt(stats, values, "13", "RBI"), StolenBases: yahooStatInt(stats, values, "16", "SB"), BattingAverage: yahooStat(stats, values, "3", "AVG"), InningsPitched: yahooStat(stats, values, "50", "IP"), Wins: yahooStatInt(stats, values, "28", "W"), Saves: yahooStatInt(stats, values, "32", "SV"), Strikeouts: yahooStatInt(stats, values, "42", "K"), EarnedRunAverage: yahooStat(stats, values, "26", "ERA"), WHIP: yahooStat(stats, values, "27", "WHIP")})
	}
	if len(players) == 0 {
		return domain.RosterWeekStats{}, yahooFantasyError(YahooIncompleteError, "weekly roster contains no players")
	}
	return domain.RosterWeekStats{TeamKey: teamKey, TeamName: domain.CleanFantasyTeamName(yahooText(team, "name")), Week: week, Players: players}, nil
}

// BoundedPageStarts returns deterministic offsets within Yahoo's call budget.
func BoundedPageStarts(total, pageSize int) ([]int, error) {
	if pageSize <= 0 {
		return nil, yahooFantasyError(YahooInvalidInputError, "page size must be positive")
	}
	pages := (total + pageSize - 1) / pageSize
	if pages > yahooMaxPages {
		return nil, yahooFantasyError(YahooIncompleteError, "pagination exceeds the bounded page limit")
	}
	output := make([]int, pages)
	for index := range output {
		output[index] = index * pageSize
	}
	return output, nil
}
