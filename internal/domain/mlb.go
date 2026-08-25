// Package domain contains provider-neutral MLB command records.
package domain

import "sort"

// Team is one current MLB club.
type Team struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Location     string `json:"location"`
	ClubName     string `json:"club_name"`
	Abbreviation string `json:"abbreviation"`
	LeagueID     int64  `json:"league_id"`
}

// RosterPlayer is one normalized member of an MLB 40-man roster.
type RosterPlayer struct {
	TeamAbbreviation  string  `json:"team_abbreviation"`
	MLBAMID           int64   `json:"mlbam_id"`
	Name              string  `json:"name"`
	Position          string  `json:"position"`
	PrimaryType       string  `json:"primary_type"`
	Status            string  `json:"status"`
	InjuryStatus      string  `json:"injury_status"`
	GameStatus        string  `json:"game_status"`
	IsCloser          bool    `json:"is_closer"`
	JerseyNumber      string  `json:"jersey_number"`
	EligiblePositions string  `json:"eligible_positions"`
	BatSide           string  `json:"bat_side"`
	PitchHand         string  `json:"pitch_hand"`
	YahooRank         *int64  `json:"yahoo_rank"`
	Owner             *string `json:"owner"`
	InYahooPool       bool    `json:"in_yahoo_pool"`
	PlateAppearances  int64   `json:"plate_appearances"`
	OnBasePercentage  float64 `json:"on_base_percentage"`
	Runs              int64   `json:"runs"`
	HomeRuns          int64   `json:"home_runs"`
	RunsBattedIn      int64   `json:"runs_batted_in"`
	StolenBases       int64   `json:"stolen_bases"`
	BattingAverage    float64 `json:"batting_average"`
	InningsPitched    float64 `json:"innings_pitched"`
	QualityStarts     int64   `json:"quality_starts"`
	Wins              int64   `json:"wins"`
	Saves             int64   `json:"saves"`
	Strikeouts        int64   `json:"strikeouts"`
	EarnedRunAverage  float64 `json:"earned_run_average"`
	WHIP              float64 `json:"whip"`
}

// Standing is one standings row with resolved club identity.
type Standing struct {
	Team      Team   `json:"team"`
	Wins      int64  `json:"wins"`
	Losses    int64  `json:"losses"`
	GamesBack string `json:"games_back"`
}

// BattingStats contains standard team season batting statistics.
type BattingStats struct {
	PlateAppearances   int32   `json:"plate_appearances"`
	BattingAverage     float64 `json:"batting_average"`
	OnBasePercentage   float64 `json:"on_base_percentage"`
	SluggingPercentage float64 `json:"slugging_percentage"`
	OnBasePlusSlugging float64 `json:"on_base_plus_slugging"`
	HomeRuns           int32   `json:"home_runs"`
	RunsBattedIn       int32   `json:"runs_batted_in"`
	Runs               int32   `json:"runs"`
	StolenBases        int32   `json:"stolen_bases"`
	Strikeouts         int32   `json:"strikeouts"`
	Walks              int32   `json:"walks"`
}

// PitchingStats contains standard team season pitching statistics.
type PitchingStats struct {
	Games                               int32   `json:"games"`
	GamesStarted                        int32   `json:"games_started"`
	InningsPitched                      float64 `json:"innings_pitched"`
	EarnedRunAverage                    float64 `json:"earned_run_average"`
	WHIP                                float64 `json:"whip"`
	Strikeouts                          int32   `json:"strikeouts"`
	StrikeoutsPerNine                   float64 `json:"strikeouts_per_nine"`
	WalksPerNine                        float64 `json:"walks_per_nine"`
	FieldingIndependentPitching         float64 `json:"fielding_independent_pitching"`
	ExpectedFieldingIndependentPitching float64 `json:"expected_fielding_independent_pitching"`
	Wins                                int32   `json:"wins"`
	Saves                               int32   `json:"saves"`
	Holds                               int32   `json:"holds"`
	QualityStarts                       int32   `json:"quality_starts"`
	RateStrikeouts                      int32   `json:"rate_strikeouts"`
	Walks                               int32   `json:"walks"`
	BattersFaced                        int32   `json:"batters_faced"`
}

// TeamTotals aggregates season totals for one club.
type TeamTotals struct {
	Team             Team          `json:"team"`
	Batting          BattingStats  `json:"batting"`
	Pitching         PitchingStats `json:"pitching"`
	YahooPlayers     *int64        `json:"yahoo_players"`
	PlayersAvailable *int64        `json:"players_available"`
}

// SlateRow is one probable-pitcher game side pair.
type SlateRow struct {
	Date           string   `json:"date"`
	GameID         int64    `json:"game_id"`
	GameTime       string   `json:"game_time"`
	AwayTeam       string   `json:"away_team"`
	HomeTeam       string   `json:"home_team"`
	AwayPitcher    string   `json:"away_pitcher"`
	HomePitcher    string   `json:"home_pitcher"`
	WinProbability *float64 `json:"win_probability"`
	AwayFreeAgent  bool     `json:"away_free_agent"`
	HomeFreeAgent  bool     `json:"home_free_agent"`
	AwayMine       bool     `json:"away_mine"`
	HomeMine       bool     `json:"home_mine"`
}

// SortTeams applies the frozen stable name and identity order.
func SortTeams(teams []Team) {
	sort.SliceStable(teams, func(i, j int) bool {
		if teams[i].Name == teams[j].Name {
			return teams[i].ID < teams[j].ID
		}
		return teams[i].Name < teams[j].Name
	})
}
