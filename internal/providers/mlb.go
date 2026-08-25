package providers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/transport"
)

const (
	mlbTimeout   = 10 * time.Second
	mlbBodyLimit = 8 * 1024 * 1024
	ScheduleTTL  = 60 * time.Second
)

// MLBEndpoints contains the validated StatsAPI root.
type MLBEndpoints struct{ Root *url.URL }

// NewMLBEndpoints validates an injected endpoint root.
func NewMLBEndpoints(root string) (MLBEndpoints, error) {
	parsed, err := url.Parse(root)
	if err != nil || parsed.Host == "" {
		return MLBEndpoints{}, invalid("configure MLB endpoint", "endpoint is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return MLBEndpoints{}, invalid("configure MLB endpoint", "endpoint must not contain credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && endpointLoopback(parsed)) {
		return MLBEndpoints{}, invalid("configure MLB endpoint", "endpoint must use HTTPS except for loopback tests")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return MLBEndpoints{Root: parsed}, nil
}

// ProductionMLBEndpoints returns the public StatsAPI root.
func ProductionMLBEndpoints() MLBEndpoints {
	endpoints, _ := NewMLBEndpoints("https://statsapi.mlb.com/api/v1/")
	return endpoints
}

// TeamStanding is one normalized standings row.
type TeamStanding struct {
	TeamID    int64  `json:"team_id"`
	LeagueID  int64  `json:"league_id"`
	Wins      int64  `json:"wins"`
	Losses    int64  `json:"losses"`
	GamesBack string `json:"games_back"`
}

// TeamDirectoryEntry is one active MLB club.
type TeamDirectoryEntry struct {
	TeamID       int64
	Name         string
	LocationName string
	ClubName     string
	Abbreviation string
	LeagueID     int64
}

// RosterMember is one normalized 40-man role.
type RosterMember struct {
	PersonID     int64
	FullName     string
	Position     string
	PrimaryType  string
	Status       string
	JerseyNumber string
}

// LineupPlayer is one lineup identity.
type LineupPlayer struct {
	PersonID int64  `json:"person_id"`
	FullName string `json:"full_name"`
}

// Linescore contains optional live game state.
type Linescore struct {
	Inning        *int64 `json:"inning"`
	InningOrdinal string `json:"inning_ordinal"`
	InningState   string `json:"inning_state"`
	AwayRuns      int64  `json:"away_runs"`
	HomeRuns      int64  `json:"home_runs"`
}

// ScheduleGame is one normalized scheduled game.
type ScheduleGame struct {
	GameID                  int64          `json:"game_id"`
	GameDate                string         `json:"game_date"`
	DetailedState           string         `json:"detailed_state"`
	AwayTeamID              int64          `json:"away_team_id"`
	AwayTeamName            string         `json:"away_team_name"`
	HomeTeamID              int64          `json:"home_team_id"`
	HomeTeamName            string         `json:"home_team_name"`
	AwayProbablePitcherID   *int64         `json:"away_probable_pitcher_id"`
	AwayProbablePitcherName string         `json:"away_probable_pitcher_name"`
	HomeProbablePitcherID   *int64         `json:"home_probable_pitcher_id"`
	HomeProbablePitcherName string         `json:"home_probable_pitcher_name"`
	Linescore               *Linescore     `json:"linescore"`
	AwayLineup              []LineupPlayer `json:"away_lineup"`
	HomeLineup              []LineupPlayer `json:"home_lineup"`
}

// HittingStats is one provider-native hitting block.
type HittingStats struct {
	GamesPlayed        int64  `json:"gamesPlayed"`
	PlateAppearances   int64  `json:"plateAppearances"`
	AtBats             int64  `json:"atBats"`
	Hits               int64  `json:"hits"`
	HomeRuns           int64  `json:"homeRuns"`
	RBI                int64  `json:"rbi"`
	Runs               int64  `json:"runs"`
	StolenBases        int64  `json:"stolenBases"`
	Average            string `json:"avg"`
	OnBasePercentage   string `json:"obp"`
	SluggingPercentage string `json:"slg"`
	OPS                string `json:"ops"`
	Strikeouts         int64  `json:"strikeOuts"`
	Walks              int64  `json:"baseOnBalls"`
	Doubles            int64  `json:"doubles"`
	Triples            int64  `json:"triples"`
	CaughtStealing     int64  `json:"caughtStealing"`
	HitByPitch         int64  `json:"hitByPitch"`
	TotalBases         int64  `json:"totalBases"`
	SacrificeFlies     int64  `json:"sacFlies"`
	SacrificeBunts     int64  `json:"sacBunts"`
	GroundedIntoDP     int64  `json:"groundIntoDoublePlay"`
	IntentionalWalks   int64  `json:"intentionalWalks"`
	BABIP              string `json:"babip"`
}

// PitchingStats is one provider-native pitching block.
type PitchingStats struct {
	GamesPitched       int64  `json:"gamesPitched"`
	GamesStarted       int64  `json:"gamesStarted"`
	InningsPitched     string `json:"inningsPitched"`
	Wins               int64  `json:"wins"`
	Losses             int64  `json:"losses"`
	Saves              int64  `json:"saves"`
	Holds              int64  `json:"holds"`
	Strikeouts         int64  `json:"strikeOuts"`
	Walks              int64  `json:"baseOnBalls"`
	ERA                string `json:"era"`
	WHIP               string `json:"whip"`
	QualityStarts      int64  `json:"qualityStarts"`
	Runs               int64  `json:"runs"`
	HitsAllowed        int64  `json:"hits"`
	EarnedRuns         int64  `json:"earnedRuns"`
	HomeRunsAllowed    int64  `json:"homeRuns"`
	HitBatsmen         int64  `json:"hitBatsmen"`
	Balks              int64  `json:"balks"`
	WildPitches        int64  `json:"wildPitches"`
	BattersFaced       int64  `json:"battersFaced"`
	GamesFinished      int64  `json:"gamesFinished"`
	SaveOpportunities  int64  `json:"saveOpportunities"`
	BlownSaves         int64  `json:"blownSaves"`
	CompleteGames      int64  `json:"completeGames"`
	Shutouts           int64  `json:"shutouts"`
	IntentionalWalks   int64  `json:"intentionalWalks"`
	StrikeoutsPerNine  string `json:"strikeoutsPer9Inn"`
	WalksPerNine       string `json:"walksPer9Inn"`
	HitsPerNine        string `json:"hitsPer9Inn"`
	HomeRunsPerNine    string `json:"homeRunsPer9Inn"`
	StrikeoutWalkRatio string `json:"strikeoutWalkRatio"`
	InheritedRunners   int64  `json:"inheritedRunners"`
	InheritedScored    int64  `json:"inheritedRunnersScored"`
	Pickoffs           int64  `json:"pickoffs"`
	StolenBasesAllowed int64  `json:"stolenBases"`
	CaughtStealing     int64  `json:"caughtStealing"`
	NumberOfPitches    int64  `json:"numberOfPitches"`
	PitchesPerInning   string `json:"pitchesPerInning"`
}

type BulkPlayer struct {
	PersonID int64  `json:"id"`
	FullName string `json:"fullName"`
}
type BulkTeam struct {
	TeamID int64 `json:"id"`
}
type BulkPosition struct {
	PositionType string `json:"type"`
}
type BulkHittingSplit struct {
	Player   BulkPlayer   `json:"player"`
	Team     BulkTeam     `json:"team"`
	Position BulkPosition `json:"position"`
	Stat     HittingStats `json:"stat"`
}
type BulkPitchingSplit struct {
	Player   BulkPlayer    `json:"player"`
	Team     BulkTeam      `json:"team"`
	Position BulkPosition  `json:"position"`
	Stat     PitchingStats `json:"stat"`
}

// PitchingGameLogEntry is one pitcher's normalized season game-log entry.
type PitchingGameLogEntry struct {
	Date           string
	GameID         int64
	IsHome         bool
	OpponentTeamID int64
	Stat           PitchingStats
}

// QualityStartIssue describes one pitcher-specific acquisition failure.
type QualityStartIssue struct {
	PersonID int64
	Detail   string
}

// QualityStartResult contains successful positive counts and bounded partial failures.
type QualityStartResult struct {
	Counts map[int64]int64
	Issues []QualityStartIssue
}

// ScheduleCacheResult describes one raw-cache-backed schedule acquisition.
type ScheduleCacheResult struct {
	Games      []ScheduleGame
	CacheState cache.State
	WriteIssue string
}

// MLBClient acquires StatsAPI data through validated transport.
type MLBClient struct {
	http      *transport.Client
	endpoints MLBEndpoints
}

func NewMLBClient(http *transport.Client, endpoints MLBEndpoints) *MLBClient {
	return &MLBClient{http: http, endpoints: endpoints}
}
func NewProductionMLBClient(http *transport.Client) *MLBClient {
	return NewMLBClient(http, ProductionMLBEndpoints())
}

// FetchTeamDirectory fetches and validates the 30-club directory.
func (client *MLBClient) FetchTeamDirectory(season int64) ([]TeamDirectoryEntry, error) {
	if err := validateSeason(season); err != nil {
		return nil, err
	}
	var response struct {
		Teams *[]struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			LocationName string `json:"locationName"`
			ClubName     string `json:"teamName"`
			Abbreviation string `json:"abbreviation"`
			League       struct {
				ID int64 `json:"id"`
			} `json:"league"`
		} `json:"teams"`
	}
	if err := client.getJSON("fetch MLB team directory", []string{"teams"}, url.Values{"sportId": {"1"}, "season": {strconv.FormatInt(season, 10)}}, &response); err != nil {
		return nil, err
	}
	if response.Teams == nil {
		return nil, invalid("fetch MLB team directory", "teams envelope is absent")
	}
	var output []TeamDirectoryEntry
	for _, team := range *response.Teams {
		abbreviation := strings.ToUpper(team.Abbreviation)
		if team.ID <= 0 || team.League.ID != 103 && team.League.ID != 104 {
			continue
		}
		output = append(output, TeamDirectoryEntry{team.ID, team.Name, team.LocationName, team.ClubName, abbreviation, team.League.ID})
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Abbreviation < output[j].Abbreviation })
	unique := map[string]struct{}{}
	for _, team := range output {
		unique[fmt.Sprintf("%d/%s", team.TeamID, team.Abbreviation)] = struct{}{}
	}
	if len(output) != 30 || len(unique) != 30 {
		return nil, invalid("fetch MLB team directory", fmt.Sprintf("expected 30 unique active clubs, received %d rows and %d unique identities", len(output), len(unique)))
	}
	return output, nil
}

// FetchStandings fetches AL and NL standings.
func (client *MLBClient) FetchStandings(season int64) ([]TeamStanding, error) {
	if err := validateSeason(season); err != nil {
		return nil, err
	}
	var response struct {
		Records *[]struct {
			League struct {
				ID int64 `json:"id"`
			} `json:"league"`
			TeamRecords []struct {
				Team struct {
					ID int64 `json:"id"`
				} `json:"team"`
				Wins      int64  `json:"wins"`
				Losses    int64  `json:"losses"`
				GamesBack string `json:"gamesBack"`
			} `json:"teamRecords"`
		} `json:"records"`
	}
	if err := client.getJSON("fetch MLB standings", []string{"standings"}, url.Values{"leagueId": {"103,104"}, "season": {strconv.FormatInt(season, 10)}}, &response); err != nil {
		return nil, err
	}
	if response.Records == nil {
		return nil, invalid("fetch MLB standings", "records envelope is absent")
	}
	var output []TeamStanding
	for _, record := range *response.Records {
		for _, row := range record.TeamRecords {
			if row.Team.ID > 0 {
				output = append(output, TeamStanding{row.Team.ID, record.League.ID, row.Wins, row.Losses, row.GamesBack})
			}
		}
	}
	return output, nil
}

// FetchRoster fetches one normalized 40-man roster.
func (client *MLBClient) FetchRoster(teamID int64) ([]RosterMember, error) {
	if teamID <= 0 {
		return nil, invalid("validate MLB identifier", "team ID must be positive")
	}
	var response struct {
		Roster *[]struct {
			Person struct {
				ID       int64  `json:"id"`
				FullName string `json:"fullName"`
			} `json:"person"`
			Position struct {
				Abbreviation string `json:"abbreviation"`
			} `json:"position"`
			Status struct {
				Code string `json:"code"`
			} `json:"status"`
			JerseyNumber string `json:"jerseyNumber"`
		} `json:"roster"`
	}
	if err := client.getJSON("fetch MLB 40-man roster", []string{"teams", strconv.FormatInt(teamID, 10), "roster"}, url.Values{"rosterType": {"40Man"}}, &response); err != nil {
		return nil, err
	}
	if response.Roster == nil {
		return nil, invalid("fetch MLB 40-man roster", "roster envelope is absent")
	}
	var output []RosterMember
	for _, row := range *response.Roster {
		if row.Person.ID <= 0 {
			continue
		}
		position := strings.ToUpper(strings.TrimSpace(row.Position.Abbreviation))
		status := strings.ToUpper(strings.TrimSpace(row.Status.Code))
		if status == "" {
			status = "A"
		}
		base := RosterMember{row.Person.ID, row.Person.FullName, position, "H", status, strings.TrimSpace(row.JerseyNumber)}
		if position == "TWP" {
			output = append(output, base)
			base.PrimaryType = "P"
			output = append(output, base)
		} else {
			if position == "P" || position == "SP" || position == "RP" {
				base.PrimaryType = "P"
			}
			output = append(output, base)
		}
	}
	return output, nil
}

// FetchSchedule fetches one day's hydrated schedule.
func (client *MLBClient) FetchSchedule(date string) ([]ScheduleGame, error) {
	if err := validateDate(date); err != nil {
		return nil, err
	}
	payload, err := client.fetchBytes("fetch MLB schedule", []string{"schedule"}, url.Values{"sportId": {"1"}, "date": {date}, "hydrate": {"linescore,probablePitcher,lineups"}})
	if err != nil {
		return nil, err
	}
	return decodeSchedule(payload)
}

// FetchScheduleCached uses the compatible bounded raw-payload cache.
func (client *MLBClient) FetchScheduleCached(date string, disk *cache.Disk) (ScheduleCacheResult, error) {
	if err := validateDate(date); err != nil {
		return ScheduleCacheResult{}, err
	}
	key := "schedule-" + date
	lookup, err := disk.Get("mlb", key, ScheduleTTL)
	if err != nil {
		return ScheduleCacheResult{}, operationError("fetch cached MLB schedule", "read schedule cache", err)
	}
	cacheState := lookup.State
	if lookup.State == cache.Hit {
		if games, decodeErr := decodeSchedule(lookup.Entry.Payload); decodeErr == nil {
			return ScheduleCacheResult{Games: games, CacheState: cache.Hit}, nil
		}
		cacheState = cache.Corrupt
	} else if lookup.State == cache.Expired {
		if _, decodeErr := decodeSchedule(lookup.Entry.Payload); decodeErr != nil {
			cacheState = cache.Corrupt
		}
	}
	payload, err := client.fetchBytes("fetch MLB schedule", []string{"schedule"}, url.Values{"sportId": {"1"}, "date": {date}, "hydrate": {"linescore,probablePitcher,lineups"}})
	if err != nil {
		return ScheduleCacheResult{}, err
	}
	games, err := decodeSchedule(payload)
	if err != nil {
		return ScheduleCacheResult{}, err
	}
	result := ScheduleCacheResult{Games: games, CacheState: cacheState}
	if err := disk.Put("mlb", key, payload); err != nil {
		result.WriteIssue = bounded(err.Error(), 256)
	}
	return result, nil
}

// FetchBulkHittingStats fetches complete season hitting splits.
func (client *MLBClient) FetchBulkHittingStats(season int64, gameType string) ([]BulkHittingSplit, error) {
	var response struct {
		Stats *[]struct {
			Splits []BulkHittingSplit `json:"splits"`
		} `json:"stats"`
	}
	if err := client.bulk("fetch MLB bulk hitting stats", season, gameType, "hitting", &response); err != nil {
		return nil, err
	}
	if response.Stats == nil || len(*response.Stats) == 0 {
		return nil, nil
	}
	return (*response.Stats)[0].Splits, nil
}

// FetchBulkPitchingStats fetches complete season pitching splits.
func (client *MLBClient) FetchBulkPitchingStats(season int64, gameType string) ([]BulkPitchingSplit, error) {
	var response struct {
		Stats *[]struct {
			Splits []BulkPitchingSplit `json:"splits"`
		} `json:"stats"`
	}
	if err := client.bulk("fetch MLB bulk pitching stats", season, gameType, "pitching", &response); err != nil {
		return nil, err
	}
	if response.Stats == nil || len(*response.Stats) == 0 {
		return nil, nil
	}
	return (*response.Stats)[0].Splits, nil
}

// FetchPitcherGameLog fetches one pitcher's chronological season game log.
func (client *MLBClient) FetchPitcherGameLog(personID, season int64) ([]PitchingGameLogEntry, error) {
	if personID <= 0 {
		return nil, invalid("validate MLB identifier", "person ID must be positive")
	}
	if err := validateSeason(season); err != nil {
		return nil, err
	}
	var response struct {
		Stats *[]struct {
			Splits []struct {
				Date string        `json:"date"`
				Stat PitchingStats `json:"stat"`
				Game struct {
					GameID int64 `json:"gamePk"`
				} `json:"game"`
				IsHome   bool `json:"isHome"`
				Opponent struct {
					TeamID int64 `json:"id"`
				} `json:"opponent"`
			} `json:"splits"`
		} `json:"stats"`
	}
	if err := client.getJSON("fetch MLB pitcher game log", []string{"people", strconv.FormatInt(personID, 10), "stats"}, url.Values{
		"stats":  {"gameLog"},
		"season": {strconv.FormatInt(season, 10)},
		"group":  {"pitching"},
	}, &response); err != nil {
		return nil, err
	}
	if response.Stats == nil || len(*response.Stats) == 0 {
		return []PitchingGameLogEntry{}, nil
	}
	rows := (*response.Stats)[0].Splits
	output := make([]PitchingGameLogEntry, 0, len(rows))
	for _, row := range rows {
		output = append(output, PitchingGameLogEntry{
			Date:           row.Date,
			GameID:         row.Game.GameID,
			IsHome:         row.IsHome,
			OpponentTeamID: row.Opponent.TeamID,
			Stat:           row.Stat,
		})
	}
	return output, nil
}

// FetchQualityStartsByDateRange derives positive quality-start counts for an inclusive date range.
func (client *MLBClient) FetchQualityStartsByDateRange(season int64, startDate, endDate string, personIDs []int64) (QualityStartResult, error) {
	if err := validateDateRange(season, startDate, endDate); err != nil {
		return QualityStartResult{}, err
	}
	return client.aggregateQualityStarts(personIDs, func(personID int64) (int64, error) {
		entries, err := client.FetchPitcherGameLog(personID, season)
		if err != nil {
			return 0, err
		}
		var count int64
		for _, entry := range entries {
			if entry.Date < startDate || entry.Date > endDate || entry.Stat.GamesStarted != 1 || entry.Stat.EarnedRuns > 3 {
				continue
			}
			innings, valid := parseInningsPitched(entry.Stat.InningsPitched)
			if valid && innings >= 6 {
				count++
			}
		}
		return count, nil
	})
}

// FetchQualityStarts derives season totals from deduplicated pitcher game logs.
func (client *MLBClient) FetchQualityStarts(season int64, personIDs []int64) (QualityStartResult, error) {
	if err := validateSeason(season); err != nil {
		return QualityStartResult{}, err
	}
	return client.FetchQualityStartsByDateRange(season, fmt.Sprintf("%04d-01-01", season), fmt.Sprintf("%04d-12-31", season), personIDs)
}

func (client *MLBClient) aggregateQualityStarts(personIDs []int64, fetch func(int64) (int64, error)) (QualityStartResult, error) {
	seen := make(map[int64]struct{}, len(personIDs))
	unique := make([]int64, 0, len(personIDs))
	for _, personID := range personIDs {
		if _, exists := seen[personID]; exists {
			continue
		}
		seen[personID] = struct{}{}
		unique = append(unique, personID)
	}
	for _, personID := range unique {
		if personID <= 0 {
			return QualityStartResult{}, invalid("validate MLB identifier", "person ID must be positive")
		}
	}
	result := QualityStartResult{Counts: make(map[int64]int64)}
	type outcome struct {
		count    int64
		err      error
		panicked bool
	}
	for start := 0; start < len(unique); start += 5 {
		end := start + 5
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		outcomes := make([]outcome, len(batch))
		var workers sync.WaitGroup
		workers.Add(len(batch))
		for index, personID := range batch {
			go func() {
				defer workers.Done()
				defer func() {
					if recover() != nil {
						outcomes[index].panicked = true
					}
				}()
				outcomes[index].count, outcomes[index].err = fetch(personID)
			}()
		}
		workers.Wait()
		for index, personID := range batch {
			outcome := outcomes[index]
			switch {
			case outcome.panicked:
				result.Issues = append(result.Issues, QualityStartIssue{PersonID: personID, Detail: "quality-start worker did not complete normally; retry the acquisition"})
			case outcome.err != nil:
				result.Issues = append(result.Issues, QualityStartIssue{PersonID: personID, Detail: bounded(outcome.err.Error(), 256)})
			case outcome.count > 0:
				result.Counts[personID] = outcome.count
			}
		}
	}
	return result, nil
}

func (client *MLBClient) bulk(operation string, season int64, gameType, group string, output any) error {
	if err := validateSeason(season); err != nil {
		return err
	}
	if gameType != "R" && gameType != "S" {
		return invalid("validate MLB game type", "game type must be R or S")
	}
	return client.getJSON(operation, []string{"stats"}, url.Values{"stats": {"season"}, "group": {group}, "gameType": {gameType}, "season": {strconv.FormatInt(season, 10)}, "playerPool": {"All"}, "limit": {"2000"}}, output)
}

func (client *MLBClient) getJSON(operation string, segments []string, query url.Values, output any) error {
	payload, err := client.fetchBytes(operation, segments, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return operationError(operation, "decode JSON response", err)
	}
	return nil
}

func (client *MLBClient) fetchBytes(operation string, segments []string, query url.Values) ([]byte, error) {
	target := *client.endpoints.Root
	var path strings.Builder
	path.WriteString(strings.TrimSuffix(target.Path, "/"))
	for _, segment := range segments {
		path.WriteString("/" + url.PathEscape(segment))
	}
	target.Path = path.String()
	target.RawQuery = query.Encode()
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: target.String(), Timeout: mlbTimeout, BodyLimit: mlbBodyLimit})
	if err != nil {
		return nil, operationError(operation, "request failed", err)
	}
	if response.Status != 200 {
		return nil, invalid(operation, fmt.Sprintf("provider returned HTTP %d", response.Status))
	}
	return response.Body, nil
}

func decodeSchedule(payload []byte) ([]ScheduleGame, error) {
	var response struct {
		Dates *[]struct {
			Games []struct {
				GameID   int64  `json:"gamePk"`
				GameDate string `json:"gameDate"`
				Status   struct {
					DetailedState string `json:"detailedState"`
				} `json:"status"`
				Teams struct {
					Away scheduleSide `json:"away"`
					Home scheduleSide `json:"home"`
				} `json:"teams"`
				Linescore *struct {
					CurrentInning *int64 `json:"currentInning"`
					Ordinal       string `json:"currentInningOrdinal"`
					State         string `json:"inningState"`
					Teams         struct {
						Away struct {
							Runs int64 `json:"runs"`
						} `json:"away"`
						Home struct {
							Runs int64 `json:"runs"`
						} `json:"home"`
					} `json:"teams"`
				} `json:"linescore"`
				Lineups *struct {
					Away []lineupWire `json:"awayPlayers"`
					Home []lineupWire `json:"homePlayers"`
				} `json:"lineups"`
			} `json:"games"`
		} `json:"dates"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, operationError("fetch MLB schedule", "decode JSON response", err)
	}
	if response.Dates == nil {
		return nil, invalid("fetch MLB schedule", "dates envelope is absent")
	}
	var output []ScheduleGame
	for _, date := range *response.Dates {
		for _, game := range date.Games {
			if game.GameID <= 0 || game.Teams.Away.Team.ID <= 0 || game.Teams.Home.Team.ID <= 0 {
				continue
			}
			row := ScheduleGame{GameID: game.GameID, GameDate: game.GameDate, DetailedState: game.Status.DetailedState, AwayTeamID: game.Teams.Away.Team.ID, AwayTeamName: game.Teams.Away.Team.displayName(), HomeTeamID: game.Teams.Home.Team.ID, HomeTeamName: game.Teams.Home.Team.displayName()}
			if game.Teams.Away.Probable.ID > 0 {
				value := game.Teams.Away.Probable.ID
				row.AwayProbablePitcherID = &value
			}
			row.AwayProbablePitcherName = game.Teams.Away.Probable.displayName()
			if game.Teams.Home.Probable.ID > 0 {
				value := game.Teams.Home.Probable.ID
				row.HomeProbablePitcherID = &value
			}
			row.HomeProbablePitcherName = game.Teams.Home.Probable.displayName()
			if game.Linescore != nil {
				row.Linescore = &Linescore{game.Linescore.CurrentInning, game.Linescore.Ordinal, game.Linescore.State, game.Linescore.Teams.Away.Runs, game.Linescore.Teams.Home.Runs}
			}
			if game.Lineups != nil {
				row.AwayLineup = convertLineup(game.Lineups.Away)
				row.HomeLineup = convertLineup(game.Lineups.Home)
			}
			output = append(output, row)
		}
	}
	return output, nil
}

type scheduleSide struct {
	Team     namedIDWire `json:"team"`
	Probable namedIDWire `json:"probablePitcher"`
}

type namedIDWire struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"fullName"`
}

func (wire namedIDWire) displayName() string {
	if wire.Name != "" {
		return wire.Name
	}
	return wire.FullName
}

type lineupWire struct {
	ID       int64  `json:"id"`
	FullName string `json:"fullName"`
}

func convertLineup(input []lineupWire) []LineupPlayer {
	output := make([]LineupPlayer, 0, len(input))
	for _, player := range input {
		if player.ID > 0 {
			output = append(output, LineupPlayer{player.ID, player.FullName})
		}
	}
	return output
}

func validateSeason(season int64) error {
	if season < 1876 || season > 9999 {
		return invalid("validate MLB season", "season must be from 1876 through 9999")
	}
	return nil
}

func validateDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value || parsed.Year() < 1876 {
		return invalid("validate MLB schedule date", "date must be a real MLB calendar date in YYYY-MM-DD form")
	}
	return nil
}

func validateDateRange(season int64, startDate, endDate string) error {
	if err := validateSeason(season); err != nil {
		return err
	}
	if err := validateDate(startDate); err != nil {
		return err
	}
	if err := validateDate(endDate); err != nil {
		return err
	}
	if startDate > endDate {
		return invalid("validate MLB date range", "start date must not follow end date")
	}
	return nil
}

func parseInningsPitched(value string) (float64, bool) {
	whole, outs, found := strings.Cut(value, ".")
	if !found || whole == "" || outs != "0" && outs != "1" && outs != "2" {
		return 0, false
	}
	for _, char := range whole {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	innings, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	extraOuts := uint64(outs[0] - '0')
	return float64(innings) + float64(extraOuts)/3, true
}

func endpointLoopback(target *url.URL) bool {
	if strings.EqualFold(target.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(target.Hostname())
	return ip != nil && ip.IsLoopback()
}
