// Package domain contains provider-neutral fantasy baseball records.
package domain

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// ScoringType identifies a fantasy-league scoring format.
type ScoringType string

const (
	ScoringRotisserie ScoringType = "rotisserie"
	ScoringHeadToHead ScoringType = "head-to-head"
	ScoringPoints     ScoringType = "points"
)

// ParseScoringType normalizes Yahoo's known scoring labels and preserves others.
func ParseScoringType(value string) ScoringType {
	switch value {
	case "head":
		return ScoringHeadToHead
	case "point":
		return ScoringPoints
	case "roto":
		return ScoringRotisserie
	default:
		return ScoringType(value)
	}
}

func (value ScoringType) String() string { return string(value) }

// Position identifies one fantasy-roster position.
type Position string

const (
	PositionCatcher         Position = "C"
	PositionFirstBase       Position = "1B"
	PositionSecondBase      Position = "2B"
	PositionThirdBase       Position = "3B"
	PositionShortstop       Position = "SS"
	PositionOutfield        Position = "OF"
	PositionStartingPitcher Position = "SP"
	PositionReliefPitcher   Position = "RP"
	PositionUtility         Position = "Util"
	PositionBench           Position = "BN"
	PositionInjuredList     Position = "IL"
)

// ParsePosition preserves known and provider-specific position labels.
func ParsePosition(value string) Position { return Position(value) }

func (value Position) String() string { return string(value) }

// League is fantasy-league metadata and scoring configuration.
type League struct {
	LeagueKey          string      `json:"league_key"`
	Name               string      `json:"name"`
	Season             int         `json:"season"`
	NumTeams           int         `json:"num_teams"`
	ScoringType        ScoringType `json:"scoring_type"`
	RosterPositions    []Position  `json:"roster_positions"`
	BattingCategories  []string    `json:"batting_categories"`
	PitchingCategories []string    `json:"pitching_categories"`
}

// FantasyTeam is one provider-neutral fantasy team.
type FantasyTeam struct {
	TeamKey               string `json:"team_key"`
	LeagueKey             string `json:"league_key"`
	TeamID                int64  `json:"team_id"`
	Name                  string `json:"name"`
	ManagerName           string `json:"manager_name"`
	IsOwnedByCurrentLogin bool   `json:"is_owned_by_current_login"`
	WaiverPriority        int64  `json:"waiver_priority"`
	FAABBalance           int64  `json:"faab_balance"`
	Wins                  int64  `json:"wins"`
	Losses                int64  `json:"losses"`
	Ties                  int64  `json:"ties"`
	Moves                 int64  `json:"moves"`
	Rank                  int64  `json:"rank"`
}

// FantasyPlayer is one provider-neutral fantasy player.
type FantasyPlayer struct {
	YahooPlayerID     int64      `json:"yahoo_player_id"`
	Name              string     `json:"name"`
	MLBTeam           string     `json:"mlb_team"`
	DisplayPosition   string     `json:"display_position"`
	PositionType      string     `json:"position_type"`
	EligiblePositions []Position `json:"eligible_positions"`
	InjuryStatus      string     `json:"injury_status"`
	PercentOwned      *float64   `json:"percent_owned"`
	PercentageStarted *float64   `json:"percentage_started"`
	YahooRank         *int64     `json:"yahoo_rank"`
}

// FantasyRosterSlot associates one player with one team's assigned slot.
type FantasyRosterSlot struct {
	TeamKey       string   `json:"team_key"`
	YahooPlayerID int64    `json:"yahoo_player_id"`
	SlotPosition  Position `json:"slot_position"`
}

// Matchup is one head-to-head pairing for a week.
type Matchup struct {
	Week      int            `json:"week"`
	WeekStart string         `json:"week_start"`
	WeekEnd   string         `json:"week_end"`
	Status    string         `json:"status"`
	Teams     [2]MatchupTeam `json:"teams"`
}

// MatchupTeam is one fantasy team's weekly matchup state.
type MatchupTeam struct {
	TeamKey        string            `json:"team_key"`
	TeamID         int64             `json:"team_id"`
	Name           string            `json:"name"`
	IsCurrentLogin bool              `json:"is_current_login"`
	Stats          map[string]string `json:"stats"`
	Wins           int               `json:"wins"`
	Losses         int               `json:"losses"`
	Ties           int               `json:"ties"`
	CompletedGames int               `json:"completed_games"`
	LiveGames      int               `json:"live_games"`
	RemainingGames int               `json:"remaining_games"`
}

func (team MatchupTeam) Score() int { return team.Wins }
func (team MatchupTeam) TotalGames() int {
	return team.CompletedGames + team.LiveGames + team.RemainingGames
}

// PlayerWeekStats is one player's weekly statistics and roster state.
type PlayerWeekStats struct {
	YahooPlayerID     int64      `json:"yahoo_player_id"`
	Name              string     `json:"name"`
	Team              string     `json:"team"`
	PositionType      string     `json:"position_type"`
	SlotPosition      Position   `json:"slot_position"`
	EligiblePositions []Position `json:"eligible_positions"`
	InjuryStatus      string     `json:"injury_status"`
	HAB               string     `json:"hab"`
	Runs              int        `json:"runs"`
	HomeRuns          int        `json:"home_runs"`
	RunsBattedIn      int        `json:"runs_batted_in"`
	StolenBases       int        `json:"stolen_bases"`
	BattingAverage    string     `json:"batting_average"`
	InningsPitched    string     `json:"innings_pitched"`
	Wins              int        `json:"wins"`
	Saves             int        `json:"saves"`
	Strikeouts        int        `json:"strikeouts"`
	EarnedRunAverage  string     `json:"earned_run_average"`
	WHIP              string     `json:"whip"`
}

// RosterWeekStats contains one team's weekly player boxscore.
type RosterWeekStats struct {
	TeamKey  string            `json:"team_key"`
	TeamName string            `json:"team_name"`
	Week     int               `json:"week"`
	Players  []PlayerWeekStats `json:"players"`
}

func (roster RosterWeekStats) Batters() []PlayerWeekStats {
	return filterWeekPlayers(roster.Players, "B")
}

func (roster RosterWeekStats) Pitchers() []PlayerWeekStats {
	return filterWeekPlayers(roster.Players, "P")
}

func filterWeekPlayers(players []PlayerWeekStats, role string) []PlayerWeekStats {
	output := make([]PlayerWeekStats, 0, len(players))
	for _, player := range players {
		if player.PositionType == role {
			output = append(output, player)
		}
	}
	return output
}

// CleanFantasyTeamName removes emoji presentation runes while retaining text.
func CleanFantasyTeamName(value string) string {
	return strings.TrimSpace(strings.Map(func(character rune) rune {
		code := uint32(character)
		if code >= 0x2600 && code <= 0x27bf ||
			code >= 0x1f000 && code <= 0x1f02f ||
			code >= 0x1f0a0 && code <= 0x1f1ff ||
			code >= 0x1f200 && code <= 0x1f2ff ||
			code >= 0x1f300 && code <= 0x1f9ff ||
			code >= 0x1fa00 && code <= 0x1faff ||
			code == 0xfe0f || code == 0x200d || unicode.Is(unicode.Variation_Selector, character) {
			return -1
		}
		return character
	}, value))
}

// IsValidISODate accepts exact, real Gregorian dates in YYYY-MM-DD form.
func IsValidISODate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

// SortFantasyTeams applies stable provider-key order.
func SortFantasyTeams(teams []FantasyTeam) {
	sort.SliceStable(teams, func(i, j int) bool { return teams[i].TeamKey < teams[j].TeamKey })
}

// SortFantasyPlayers applies stable Yahoo identity order.
func SortFantasyPlayers(players []FantasyPlayer) {
	sort.SliceStable(players, func(i, j int) bool { return players[i].YahooPlayerID < players[j].YahooPlayerID })
}

// SortFantasyRosterSlots applies stable team, player, then slot order.
func SortFantasyRosterSlots(slots []FantasyRosterSlot) {
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].TeamKey != slots[j].TeamKey {
			return slots[i].TeamKey < slots[j].TeamKey
		}
		if slots[i].YahooPlayerID != slots[j].YahooPlayerID {
			return slots[i].YahooPlayerID < slots[j].YahooPlayerID
		}
		return slots[i].SlotPosition < slots[j].SlotPosition
	})
}
