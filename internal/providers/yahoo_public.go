package providers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/transport"
)

const (
	yahooTimeout           = 10 * time.Second
	yahooBodyLimit         = 8 * 1024 * 1024
	yahooRankBatchSize     = 50
	yahooFreeAgentPageSize = 100
	yahooMaxFreeAgentPages = 40
	yahooParallelLimit     = 4
	yahooHiddenPlaceholder = "--hidden--"
)

var yahooCountingStatIDs = []string{"1", "7", "12", "13", "16", "6", "8", "28", "32", "42", "33", "37", "39", "34"}

// YahooEndpoints contains Yahoo's three unauthenticated public roots.
type YahooEndpoints struct {
	Redzone       *url.URL
	Fantasy       *url.URL
	PublicPlayers *url.URL
}

// NewYahooEndpoints validates injected Yahoo public endpoints.
func NewYahooEndpoints(redzone, fantasy, publicPlayers string) (YahooEndpoints, error) {
	parse := func(label, value, productionHost, productionPath string) (*url.URL, error) {
		target, err := url.Parse(value)
		if err != nil || target.Host == "" {
			return nil, invalid("configure Yahoo endpoints", label+" URL is invalid")
		}
		if target.User != nil || target.RawQuery != "" || target.Fragment != "" {
			return nil, invalid("configure Yahoo endpoints", label+" URL must not contain credentials, a query, or a fragment")
		}
		if target.Scheme != "https" && !(target.Scheme == "http" && endpointLoopback(target)) {
			return nil, invalid("configure Yahoo endpoints", label+" URL must use HTTPS or loopback HTTP")
		}
		if target.Scheme == "https" && (target.Hostname() != productionHost || strings.TrimSuffix(target.Path, "/") != productionPath) {
			return nil, invalid("configure Yahoo endpoints", label+" URL must use the exact Yahoo public host and path")
		}
		return target, nil
	}
	redzoneURL, err := parse("redzone", redzone, "pub-api.fantasysports.yahoo.com", "/fantasy/v3/redzone/mlb")
	if err != nil {
		return YahooEndpoints{}, err
	}
	fantasyURL, err := parse("fantasy", fantasy, "pub-api-ro.fantasysports.yahoo.com", "/fantasy/v2")
	if err != nil {
		return YahooEndpoints{}, err
	}
	playersURL, err := parse("public players", publicPlayers, "pub-api-ro.fantasysports.yahoo.com", "/fantasy/v2/league/mlb.l.public/players")
	if err != nil {
		return YahooEndpoints{}, err
	}
	return YahooEndpoints{Redzone: redzoneURL, Fantasy: fantasyURL, PublicPlayers: playersURL}, nil
}

// ProductionYahooEndpoints returns Yahoo's exact public-only endpoints.
func ProductionYahooEndpoints() YahooEndpoints {
	endpoints, _ := NewYahooEndpoints(
		"https://pub-api.fantasysports.yahoo.com/fantasy/v3/redzone/mlb",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.public/players",
	)
	return endpoints
}

// YahooPublicErrorKind classifies one public-feed failure.
type YahooPublicErrorKind string

const (
	YahooInvalidLeagueKey YahooPublicErrorKind = "invalid_league_key"
	YahooRequestError     YahooPublicErrorKind = "request"
	YahooBlockedError     YahooPublicErrorKind = "blocked"
	YahooMalformedError   YahooPublicErrorKind = "malformed"
	YahooPublicIncomplete YahooPublicErrorKind = "incomplete"
)

// YahooPublicError carries bounded public-feed context and optional HTTP status.
type YahooPublicError struct {
	Kind   YahooPublicErrorKind
	Detail string
	Status int
}

func (failure *YahooPublicError) Error() string {
	switch failure.Kind {
	case YahooInvalidLeagueKey:
		return fmt.Sprintf("resolve public league id: %q is not a bare number or a {game_key}.l.{league_id} key; provide the numeric league id and retry", failure.Detail)
	case YahooRequestError:
		return "request Yahoo public feed: " + failure.Detail + "; verify connectivity and retry"
	case YahooBlockedError:
		return fmt.Sprintf("Yahoo public feed returned HTTP %d; the feed may be temporarily unavailable or the league id may be wrong — retry later", failure.Status)
	case YahooMalformedError:
		return "parse Yahoo public feed: " + failure.Detail + "; the feed shape may have changed"
	default:
		return "Yahoo public feed response is incomplete: " + failure.Detail + "; prior local data was retained"
	}
}

func yahooPublicError(kind YahooPublicErrorKind, detail string) error {
	return &YahooPublicError{Kind: kind, Detail: bounded(detail, 512)}
}

func yahooDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// LeagueIDFromKey extracts a numeric league id from every supported key form.
func LeagueIDFromKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if yahooDigits(trimmed) {
		return trimmed, nil
	}
	for _, prefix := range []string{"mlb.l.", "public."} {
		if leagueID, found := strings.CutPrefix(trimmed, prefix); found && yahooDigits(leagueID) {
			return leagueID, nil
		}
	}
	parts := strings.Split(trimmed, ".l.")
	if len(parts) == 2 && yahooDigits(parts[0]) && yahooDigits(parts[1]) {
		return parts[1], nil
	}
	return "", &YahooPublicError{Kind: YahooInvalidLeagueKey, Detail: value}
}

// CanonicalPublicLeagueKey normalizes a league id to Yahoo's public season alias.
func CanonicalPublicLeagueKey(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	parts := strings.Split(trimmed, ".l.")
	if len(parts) == 2 && (parts[0] == "mlb" || yahooDigits(parts[0])) && yahooDigits(parts[1]) {
		return trimmed, nil
	}
	leagueID, err := LeagueIDFromKey(trimmed)
	if err != nil {
		return "", err
	}
	return "mlb.l." + leagueID, nil
}

func validYahooLeagueKey(value string) bool {
	parts := strings.Split(value, ".l.")
	return len(parts) == 2 && (parts[0] == "mlb" || yahooDigits(parts[0])) && yahooDigits(parts[1])
}

func validYahooTeamKey(value string) bool {
	league, team, found := strings.Cut(value, ".t.")
	return found && validYahooLeagueKey(league) && yahooDigits(team)
}

// RedzoneFeed is one complete public league snapshot.
type RedzoneFeed struct {
	League          domain.League                     `json:"league"`
	Teams           []domain.FantasyTeam              `json:"teams"`
	Players         []domain.FantasyPlayer            `json:"players"`
	Slots           []domain.FantasyRosterSlot        `json:"slots"`
	Matchups        []domain.Matchup                  `json:"matchups"`
	RosterWeekStats map[string]domain.RosterWeekStats `json:"roster_week_stats"`
	Week            int                               `json:"week"`
	RosterPositions []RosterPosition                  `json:"roster_positions"`
}

// YahooPublicClient acquires Yahoo data without credentials or browser state.
type YahooPublicClient struct {
	http      *transport.Client
	endpoints YahooEndpoints
}

func NewYahooPublicClient(http *transport.Client, endpoints YahooEndpoints) *YahooPublicClient {
	return &YahooPublicClient{http: http, endpoints: endpoints}
}

func NewProductionYahooPublicClient(http *transport.Client) *YahooPublicClient {
	return NewYahooPublicClient(http, ProductionYahooEndpoints())
}

func (client *YahooPublicClient) execute(target string) ([]byte, int, error) {
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: target, Headers: []transport.Header{{Name: "Accept", Value: "application/json"}}, Timeout: yahooTimeout, BodyLimit: yahooBodyLimit})
	if err != nil {
		return nil, 0, err
	}
	return response.Body, response.Status, nil
}

func (client *YahooPublicClient) getJSON(target string) ([]byte, error) {
	payload, status, err := client.execute(target)
	if err != nil {
		return nil, yahooFantasyError(YahooProviderError, err.Error())
	}
	if status != 200 {
		return nil, yahooFantasyError(YahooProviderError, fmt.Sprintf("Yahoo public endpoint returned HTTP %d; retry later", status))
	}
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return nil, yahooFantasyError(YahooInvalidPayloadError, "response is not valid JSON")
	}
	return payload, nil
}

func yahooJoin(root *url.URL, suffix string) string {
	return strings.TrimSuffix(root.String(), "/") + suffix
}

// LeagueSettings fetches one league's complete public settings.
func (client *YahooPublicClient) LeagueSettings(leagueKey string) (LeagueSettings, error) {
	if !validYahooLeagueKey(leagueKey) {
		return LeagueSettings{}, yahooFantasyError(YahooInvalidInputError, "league key is invalid")
	}
	payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, "/league/"+leagueKey+"/settings?format=json"))
	if err != nil {
		return LeagueSettings{}, err
	}
	return ParseLeagueSettings(leagueKey, payload)
}

// Standings fetches one league's complete public standings.
func (client *YahooPublicClient) Standings(leagueKey string) ([]domain.FantasyTeam, error) {
	if !validYahooLeagueKey(leagueKey) {
		return nil, yahooFantasyError(YahooInvalidInputError, "league key is invalid")
	}
	payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, "/league/"+leagueKey+"/standings?format=json"))
	if err != nil {
		return nil, err
	}
	return ParseStandings(leagueKey, payload)
}

// LeagueRosters fetches each team separately because Yahoo may omit echoed keys.
func (client *YahooPublicClient) LeagueRosters(_ string, teamKeys []string, progress YahooPlayerProgress) (LeagueRosters, error) {
	if len(teamKeys) == 0 {
		return LeagueRosters{}, yahooFantasyError(YahooIncompleteError, "league roster has no team keys")
	}
	for _, teamKey := range teamKeys {
		if !validYahooTeamKey(teamKey) {
			return LeagueRosters{}, yahooFantasyError(YahooInvalidInputError, "team key is invalid")
		}
	}
	type rosterResult struct {
		index   int
		payload []byte
		players []domain.FantasyPlayer
		err     error
	}
	workerCount := min(yahooParallelLimit, len(teamKeys))
	jobs := make(chan int, workerCount)
	results := make(chan rosterResult, workerCount)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				teamKey := teamKeys[index]
				payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, "/team/"+teamKey+"/roster/players;out=ranks,percent_owned,percent_started?format=json"))
				result := rosterResult{index: index, payload: payload, err: err}
				if err == nil {
					parsed, parseErr := ParseTeamRosters([]string{teamKey}, [][]byte{payload})
					result.err = parseErr
					result.players = parsed.Players
				}
				results <- result
			}
		}()
	}
	payloads := make([][]byte, len(teamKeys))
	next, outstanding := 0, 0
	for outstanding < workerCount && next < len(teamKeys) {
		jobs <- next
		next++
		outstanding++
	}
	seen := make(map[int64]struct{})
	lastCount := 0
	var firstErr error
	for outstanding > 0 {
		result := <-results
		outstanding--
		if firstErr == nil {
			if result.err != nil {
				firstErr = result.err
			} else {
				payloads[result.index] = result.payload
				for _, player := range result.players {
					seen[player.YahooPlayerID] = struct{}{}
				}
				if progress != nil && len(seen) != lastCount {
					lastCount = len(seen)
					progress(lastCount)
				}
				if next < len(teamKeys) {
					jobs <- next
					next++
					outstanding++
				}
			}
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		return LeagueRosters{}, firstErr
	}
	rosters, err := ParseTeamRosters(teamKeys, payloads)
	if err != nil {
		return LeagueRosters{}, err
	}
	if progress != nil && len(rosters.Players) > lastCount {
		progress(len(rosters.Players))
	}
	return rosters, nil
}

// FreeAgents fetches all available players through bounded pagination.
func (client *YahooPublicClient) FreeAgents(leagueKey string, progress YahooPlayerProgress) ([]domain.FantasyPlayer, error) {
	if !validYahooLeagueKey(leagueKey) {
		return nil, yahooFantasyError(YahooInvalidInputError, "league key is invalid")
	}
	unique := map[int64]domain.FantasyPlayer{}
	lastCount := 0
	for page := 0; page <= yahooMaxFreeAgentPages; page++ {
		offset := page * yahooFreeAgentPageSize
		path := fmt.Sprintf("/league/%s/players;status=A;start=%d;count=%d;out=ranks,percent_owned,percent_started?format=json", leagueKey, offset, yahooFreeAgentPageSize)
		payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, path))
		if err != nil {
			return nil, err
		}
		rows, err := ParseFreeAgents(payload)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			if len(unique) == 0 {
				return nil, yahooFantasyError(YahooIncompleteError, "available-player pages contain no players")
			}
			output := make([]domain.FantasyPlayer, 0, len(unique))
			for _, player := range unique {
				output = append(output, player)
			}
			domain.SortFantasyPlayers(output)
			return output, nil
		}
		if page == yahooMaxFreeAgentPages {
			return nil, yahooFantasyError(YahooIncompleteError, "available-player collection exceeds 4,000 players")
		}
		for _, player := range rows {
			unique[player.YahooPlayerID] = player
		}
		if progress != nil && len(unique) != lastCount {
			lastCount = len(unique)
			progress(lastCount)
		}
	}
	panic("bounded Yahoo pagination did not terminate")
}

// Scoreboard fetches one optional Yahoo scoring week.
func (client *YahooPublicClient) Scoreboard(leagueKey string, week *int) ([]domain.Matchup, error) {
	if !validYahooLeagueKey(leagueKey) {
		return nil, yahooFantasyError(YahooInvalidInputError, "league key is invalid")
	}
	suffix := ""
	if week != nil {
		if *week <= 0 {
			return nil, yahooFantasyError(YahooInvalidInputError, "week must be positive")
		}
		suffix = fmt.Sprintf(";week=%d", *week)
	}
	payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, "/league/"+leagueKey+"/scoreboard"+suffix+"?format=json"))
	if err != nil {
		return nil, err
	}
	return ParseScoreboard(payload)
}

// RosterWeekStats fetches one team's player statistics for a week.
func (client *YahooPublicClient) RosterWeekStats(teamKey string, week int) (domain.RosterWeekStats, error) {
	if !validYahooTeamKey(teamKey) {
		return domain.RosterWeekStats{}, yahooFantasyError(YahooInvalidInputError, "team key is invalid")
	}
	if week <= 0 {
		return domain.RosterWeekStats{}, yahooFantasyError(YahooInvalidInputError, "week must be positive")
	}
	path := fmt.Sprintf("/team/%s/roster;week=%d/players/stats;type=week;week=%d?format=json", teamKey, week, week)
	payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, path))
	if err != nil {
		return domain.RosterWeekStats{}, err
	}
	return ParseRosterWeekStats(teamKey, week, payload)
}

// RosterDayStats is dormant acquisition groundwork for the later matchup command.
func (client *YahooPublicClient) RosterDayStats(teamKey string, week int, day string) (domain.RosterWeekStats, error) {
	if !validYahooTeamKey(teamKey) || week <= 0 || !domain.IsValidISODate(day) {
		return domain.RosterWeekStats{}, yahooFantasyError(YahooInvalidInputError, "week and day must identify a valid roster date")
	}
	path := fmt.Sprintf("/team/%s/roster;date=%s/players/stats;type=date;date=%s?format=json", teamKey, day, day)
	payload, err := client.getJSON(yahooJoin(client.endpoints.Fantasy, path))
	if err != nil {
		return domain.RosterWeekStats{}, err
	}
	return ParseRosterWeekStats(teamKey, week, payload)
}

// FetchScoreboard fetches a required authoritative public scoreboard.
func (client *YahooPublicClient) FetchScoreboard(leagueKey string, week int) ([]domain.Matchup, error) {
	if !validYahooLeagueKey(leagueKey) || week <= 0 {
		return nil, yahooPublicError(YahooInvalidLeagueKey, leagueKey)
	}
	target := yahooJoin(client.endpoints.Fantasy, fmt.Sprintf("/league/%s/scoreboard;week=%d?format=json", leagueKey, week))
	payload, status, err := client.execute(target)
	if err != nil {
		return nil, yahooPublicError(YahooRequestError, err.Error())
	}
	if status != 200 {
		return nil, &YahooPublicError{Kind: YahooBlockedError, Status: status}
	}
	matchups, err := ParseScoreboard(payload)
	if err != nil {
		return nil, yahooPublicError(YahooMalformedError, "public scoreboard has an unexpected shape: "+err.Error())
	}
	if len(matchups) == 0 {
		return nil, yahooPublicError(YahooPublicIncomplete, "public scoreboard has no matchups")
	}
	return matchups, nil
}

// FetchRedzone fetches and validates one complete public league snapshot.
func (client *YahooPublicClient) FetchRedzone(leagueID, leagueKey string) (RedzoneFeed, error) {
	if !yahooDigits(leagueID) || !validYahooLeagueKey(leagueKey) {
		return RedzoneFeed{}, yahooPublicError(YahooInvalidLeagueKey, leagueID)
	}
	target := *client.endpoints.Redzone
	query := target.Query()
	query.Set("league_id", leagueID)
	query.Set("format", "json")
	target.RawQuery = query.Encode()
	payload, status, err := client.execute(target.String())
	if err != nil {
		return RedzoneFeed{}, yahooPublicError(YahooRequestError, err.Error())
	}
	if status != 200 {
		return RedzoneFeed{}, &YahooPublicError{Kind: YahooBlockedError, Status: status}
	}
	var root yahooRawRoot
	if err := json.Unmarshal(payload, &root); err != nil {
		return RedzoneFeed{}, yahooPublicError(YahooMalformedError, "response is not the expected JSON shape")
	}
	return root.feed(leagueID, leagueKey)
}

// EnrichPlayerRanks supplements players with public season ranks in batches of 50.
func (client *YahooPublicClient) EnrichPlayerRanks(players []domain.FantasyPlayer) error {
	ranks := map[int64]int64{}
	for start := 0; start < len(players); start += yahooRankBatchSize {
		end := min(start+yahooRankBatchSize, len(players))
		ids := make([]string, 0, end-start)
		for _, player := range players[start:end] {
			ids = append(ids, strconv.FormatInt(player.YahooPlayerID, 10))
		}
		path := ";player_ids=" + strings.Join(ids, ",") + ";out=ranks;ranks=season?format=json_f"
		payload, status, err := client.execute(yahooJoin(client.endpoints.PublicPlayers, path))
		if err != nil {
			return yahooPublicError(YahooRequestError, err.Error())
		}
		if status != 200 {
			return &YahooPublicError{Kind: YahooBlockedError, Status: status}
		}
		var value any
		if json.Unmarshal(payload, &value) != nil {
			return yahooPublicError(YahooMalformedError, "public player ranks are not valid JSON")
		}
		collectYahooPublicRanks(value, ranks)
	}
	for index := range players {
		if rank, found := ranks[players[index].YahooPlayerID]; found {
			players[index].YahooRank = &rank
		}
	}
	return nil
}

// EnrichTeamTransactions supplements all teams or retains the original slice.
func (client *YahooPublicClient) EnrichTeamTransactions(leagueKey string, teams []domain.FantasyTeam) error {
	if !validYahooLeagueKey(leagueKey) {
		return yahooPublicError(YahooInvalidLeagueKey, leagueKey)
	}
	payload, status, err := client.execute(yahooJoin(client.endpoints.Fantasy, "/league/"+leagueKey+"/standings?format=json"))
	if err != nil {
		return yahooPublicError(YahooRequestError, err.Error())
	}
	if status != 200 {
		return &YahooPublicError{Kind: YahooBlockedError, Status: status}
	}
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return yahooPublicError(YahooMalformedError, "public standings are not valid JSON")
	}
	transactions := yahooPublicTeamTransactions(value)
	if len(transactions) != len(teams) {
		return yahooPublicError(YahooPublicIncomplete, "public standings do not contain every league team")
	}
	for _, team := range teams {
		if _, found := transactions[team.TeamKey]; !found {
			return yahooPublicError(YahooPublicIncomplete, "public standings team keys do not match the league roster")
		}
	}
	for index := range teams {
		row := transactions[teams[index].TeamKey]
		teams[index].WaiverPriority, teams[index].FAABBalance, teams[index].Moves = row[0], row[1], row[2]
	}
	return nil
}

type yahooRawRoot struct {
	Service yahooRawService `json:"service"`
}

type yahooRawService struct {
	Leagues map[string]yahooRawLeague       `json:"leagues"`
	Players map[string]yahooRawPlayerLookup `json:"players"`
}

type yahooRawLeague struct {
	Name          string                  `json:"name"`
	ScoringType   string                  `json:"scoringType"`
	Teams         map[string]yahooRawTeam `json:"teams"`
	WeekInfo      yahooRawWeekInfo        `json:"weekInfo"`
	Stats         []yahooRawStatMeta      `json:"stats"`
	MatchupGroups []yahooRawMatchupGroup  `json:"matchupGroups"`
	Positions     []string                `json:"positions"`
}

type yahooRawWeekInfo struct {
	Week  int    `json:"week"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type yahooRawStatMeta struct {
	ID         string `json:"id"`
	IsScoring  bool   `json:"isScoring"`
	IsNegative bool   `json:"isNegative"`
}

type yahooRawMatchupGroup struct {
	Matchups [][]string `json:"matchups"`
}

type yahooRawTeam struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Rank     string                     `json:"rank"`
	Wins     int64                      `json:"wins"`
	Losses   int64                      `json:"losses"`
	Ties     int64                      `json:"ties"`
	Managers map[string]yahooRawManager `json:"managers"`
	Players  []yahooRawRosterPlayer     `json:"players"`
}

type yahooRawManager struct {
	NickName string `json:"nickName"`
}

type yahooRawRosterPlayer struct {
	ID                    *string         `json:"id"`
	Position              string          `json:"position"`
	EligiblePositionSlots []string        `json:"eligiblePositionSlots"`
	PositionType          any             `json:"positionType"`
	Status                string          `json:"status"`
	Invalid               bool            `json:"invalid"`
	Stats                 json.RawMessage `json:"stats"`
}

func (player yahooRawRosterPlayer) statMap() map[string]any {
	output := map[string]any{}
	_ = json.Unmarshal(player.Stats, &output)
	return output
}

type yahooRawPlayerLookup struct{ Name, Team string }

func yahooRawStat(stats map[string]any, id string) float64 {
	switch value := stats[id].(type) {
	case float64:
		return value
	case string:
		parsed, _ := strconv.ParseFloat(value, 64)
		return parsed
	default:
		return 0
	}
}

func yahooRawStatString(stats map[string]any, id string) string {
	value := yahooRawStat(stats, id)
	if value == math.Trunc(value) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func yahooFormatInnings(outs float64) string {
	whole := int64(math.Round(outs))
	return fmt.Sprintf("%d.%d", whole/3, whole%3)
}

func yahooRostered(player yahooRawRosterPlayer) bool {
	return !player.Invalid && player.ID != nil && player.Position != "--"
}

func yahooAggregateRoster(team yahooRawTeam) map[string]float64 {
	sums := map[string]float64{}
	for _, player := range team.Players {
		if !yahooRostered(player) || player.Position == "BN" || player.Position == "IL" {
			continue
		}
		stats := player.statMap()
		for _, id := range yahooCountingStatIDs {
			sums[id] += yahooRawStat(stats, id)
		}
	}
	return sums
}

func yahooTeamStatsDisplay(sums map[string]float64) map[string]string {
	ab, hits := sums["6"], sums["8"]
	outs, innings := sums["33"], sums["33"]/3
	output := map[string]string{}
	for id, value := range sums {
		if id == "6" || id == "8" {
			continue
		}
		if value == math.Trunc(value) {
			output[id] = strconv.FormatInt(int64(value), 10)
		} else {
			output[id] = strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	output["3"] = "0.000"
	if ab > 0 {
		output["3"] = fmt.Sprintf("%.3f", hits/ab)
	}
	output["H/AB"] = fmt.Sprintf("%.0f/%.0f", hits, ab)
	output["26"], output["27"] = "0.00", "0.00"
	if innings > 0 {
		output["26"] = fmt.Sprintf("%.2f", 9*sums["37"]/innings)
		output["27"] = fmt.Sprintf("%.2f", (sums["39"]+sums["34"])/innings)
	}
	output["50"] = yahooFormatInnings(outs)
	delete(output, "60")
	return output
}

func yahooCompareCategories(left, right map[string]string, stats []yahooRawStatMeta) (int, int, int) {
	wins, losses, ties := 0, 0, 0
	for _, meta := range stats {
		if !meta.IsScoring {
			continue
		}
		mine, mineErr := strconv.ParseFloat(left[meta.ID], 64)
		theirs, theirsErr := strconv.ParseFloat(right[meta.ID], 64)
		if mineErr != nil || theirsErr != nil {
			continue
		}
		if mine == theirs {
			ties++
		} else if (mine > theirs) != meta.IsNegative {
			wins++
		} else {
			losses++
		}
	}
	return wins, losses, ties
}

func yahooRedzoneWeekPlayer(raw yahooRawRosterPlayer, lookup yahooRawPlayerLookup) (domain.PlayerWeekStats, bool) {
	if !yahooRostered(raw) || raw.ID == nil {
		return domain.PlayerWeekStats{}, false
	}
	id, err := strconv.ParseInt(*raw.ID, 10, 64)
	if err != nil {
		return domain.PlayerWeekStats{}, false
	}
	stats := raw.statMap()
	outs, innings := yahooRawStat(stats, "33"), yahooRawStat(stats, "33")/3
	ab, hits := yahooRawStat(stats, "6"), yahooRawStat(stats, "8")
	average, era, whip := "0.000", "0.00", "0.00"
	if ab > 0 {
		average = fmt.Sprintf("%.3f", hits/ab)
	}
	if innings > 0 {
		era = fmt.Sprintf("%.2f", 9*yahooRawStat(stats, "37")/innings)
		whip = fmt.Sprintf("%.2f", (yahooRawStat(stats, "39")+yahooRawStat(stats, "34"))/innings)
	}
	positions := make([]domain.Position, 0, len(raw.EligiblePositionSlots))
	for _, position := range raw.EligiblePositionSlots {
		positions = append(positions, domain.ParsePosition(position))
	}
	role, _ := raw.PositionType.(string)
	return domain.PlayerWeekStats{YahooPlayerID: id, Name: lookup.Name, Team: lookup.Team, PositionType: role, SlotPosition: domain.ParsePosition(raw.Position), EligiblePositions: positions, InjuryStatus: raw.Status, HAB: yahooRawStatString(stats, "8") + "-" + yahooRawStatString(stats, "6"), Runs: int(yahooRawStat(stats, "7")), HomeRuns: int(yahooRawStat(stats, "12")), RunsBattedIn: int(yahooRawStat(stats, "13")), StolenBases: int(yahooRawStat(stats, "16")), BattingAverage: average, InningsPitched: yahooFormatInnings(outs), Wins: int(yahooRawStat(stats, "28")), Saves: int(yahooRawStat(stats, "32")), Strikeouts: int(yahooRawStat(stats, "42")), EarnedRunAverage: era, WHIP: whip}, true
}

func (root yahooRawRoot) feed(leagueID, leagueKey string) (RedzoneFeed, error) {
	raw, found := root.Service.Leagues[leagueID]
	if !found {
		return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "response has no entry for the requested league id")
	}
	if len(raw.Teams) == 0 {
		return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "league has no teams")
	}
	if len(raw.WeekInfo.Start) < 4 {
		return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "week start date is missing or malformed")
	}
	season, err := strconv.Atoi(raw.WeekInfo.Start[:4])
	if err != nil {
		return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "week start date is missing or malformed")
	}
	positionCounts := []RosterPosition{}
	for _, label := range raw.Positions {
		position := domain.ParsePosition(label)
		found := false
		for index := range positionCounts {
			if positionCounts[index].Position == position {
				positionCounts[index].Count++
				found = true
			}
		}
		if !found {
			positionCounts = append(positionCounts, RosterPosition{Position: position, Count: 1})
		}
	}
	result := RedzoneFeed{League: domain.League{LeagueKey: leagueKey, Name: raw.Name, Season: season, NumTeams: len(raw.Teams), ScoringType: domain.ParseScoringType(raw.ScoringType)}, Week: raw.WeekInfo.Week, RosterPositions: positionCounts, RosterWeekStats: map[string]domain.RosterWeekStats{}}
	teamIDs := make([]string, 0, len(raw.Teams))
	for id := range raw.Teams {
		teamIDs = append(teamIDs, id)
	}
	sort.Slice(teamIDs, func(i, j int) bool {
		left, _ := strconv.Atoi(teamIDs[i])
		right, _ := strconv.Atoi(teamIDs[j])
		return left < right
	})
	players := map[int64]domain.FantasyPlayer{}
	teamStats := map[string]map[string]string{}
	for _, rawID := range teamIDs {
		team := raw.Teams[rawID]
		teamID, err := strconv.ParseInt(team.ID, 10, 64)
		if err != nil {
			return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "team id is not numeric")
		}
		teamKey := fmt.Sprintf("%s.t.%s", leagueKey, team.ID)
		manager := yahooHiddenPlaceholder
		managerKeys := make([]string, 0, len(team.Managers))
		for key := range team.Managers {
			managerKeys = append(managerKeys, key)
		}
		sort.Strings(managerKeys)
		if len(managerKeys) > 0 {
			manager = team.Managers[managerKeys[0]].NickName
		}
		rank, _ := strconv.ParseInt(team.Rank, 10, 64)
		result.Teams = append(result.Teams, domain.FantasyTeam{TeamKey: teamKey, LeagueKey: leagueKey, TeamID: teamID, Name: team.Name, ManagerName: manager, Wins: team.Wins, Losses: team.Losses, Ties: team.Ties, Rank: rank})
		week := domain.RosterWeekStats{TeamKey: teamKey, TeamName: team.Name, Week: raw.WeekInfo.Week}
		for _, player := range team.Players {
			if !yahooRostered(player) || player.ID == nil {
				continue
			}
			id, err := strconv.ParseInt(*player.ID, 10, 64)
			if err != nil {
				return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "player id is not numeric")
			}
			lookup := root.Service.Players[*player.ID]
			positions := make([]domain.Position, 0, len(player.EligiblePositionSlots))
			for _, position := range player.EligiblePositionSlots {
				positions = append(positions, domain.ParsePosition(position))
			}
			role, _ := player.PositionType.(string)
			if _, exists := players[id]; !exists {
				players[id] = domain.FantasyPlayer{YahooPlayerID: id, Name: lookup.Name, MLBTeam: lookup.Team, DisplayPosition: player.Position, PositionType: role, EligiblePositions: positions, InjuryStatus: player.Status}
			}
			result.Slots = append(result.Slots, domain.FantasyRosterSlot{TeamKey: teamKey, YahooPlayerID: id, SlotPosition: domain.ParsePosition(player.Position)})
			if stats, ok := yahooRedzoneWeekPlayer(player, lookup); ok {
				week.Players = append(week.Players, stats)
			}
		}
		result.RosterWeekStats[teamKey] = week
		teamStats[rawID] = yahooTeamStatsDisplay(yahooAggregateRoster(team))
	}
	if len(players) == 0 || len(result.Slots) == 0 {
		return RedzoneFeed{}, yahooPublicError(YahooPublicIncomplete, "league has no rostered players")
	}
	for _, player := range players {
		result.Players = append(result.Players, player)
	}
	domain.SortFantasyTeams(result.Teams)
	domain.SortFantasyPlayers(result.Players)
	domain.SortFantasyRosterSlots(result.Slots)
	for _, group := range raw.MatchupGroups {
		for _, pair := range group.Matchups {
			if len(pair) != 2 {
				continue
			}
			left, leftOK := raw.Teams[pair[0]]
			right, rightOK := raw.Teams[pair[1]]
			if !leftOK || !rightOK {
				continue
			}
			wins, losses, ties := yahooCompareCategories(teamStats[pair[0]], teamStats[pair[1]], raw.Stats)
			leftID, _ := strconv.ParseInt(pair[0], 10, 64)
			rightID, _ := strconv.ParseInt(pair[1], 10, 64)
			result.Matchups = append(result.Matchups, domain.Matchup{Week: raw.WeekInfo.Week, WeekStart: raw.WeekInfo.Start, WeekEnd: raw.WeekInfo.End, Teams: [2]domain.MatchupTeam{{TeamKey: fmt.Sprintf("%s.t.%s", leagueKey, pair[0]), TeamID: leftID, Name: left.Name, Stats: teamStats[pair[0]], Wins: wins, Losses: losses, Ties: ties}, {TeamKey: fmt.Sprintf("%s.t.%s", leagueKey, pair[1]), TeamID: rightID, Name: right.Name, Stats: teamStats[pair[1]], Wins: losses, Losses: wins, Ties: ties}}})
		}
	}
	return result, nil
}

func collectYahooPublicRanks(value any, output map[int64]int64) {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			collectYahooPublicRanks(child, output)
		}
	case map[string]any:
		flat := flattenedYahoo(value)
		playerID := yahooInt(flat, "player_id")
		if playerID > 0 {
			seasons := map[int]int64{}
			var collect func(any)
			collect = func(node any) {
				switch node := node.(type) {
				case []any:
					for _, child := range node {
						collect(child)
					}
				case map[string]any:
					if _, positional := node["rank_position"]; positional {
						return
					}
					row := flattenedYahoo(node)
					season, rank := int(yahooInt(row, "rank_season")), yahooInt(row, "rank_value")
					if season > 0 && rank > 0 {
						seasons[season] = rank
						return
					}
					for _, key := range sortedYahooKeys(node) {
						collect(node[key])
					}
				}
			}
			collect(value)
			years := make([]int, 0, len(seasons))
			for year := range seasons {
				years = append(years, year)
			}
			sort.Ints(years)
			if len(years) > 0 {
				output[playerID] = seasons[years[len(years)-1]]
			}
			return
		}
		for _, key := range sortedYahooKeys(value) {
			collectYahooPublicRanks(value[key], output)
		}
	}
}

func yahooPublicTeamTransactions(value any) map[string][3]int64 {
	output := map[string][3]int64{}
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				collect(child)
			}
		case map[string]any:
			if team, found := value["team"]; found {
				row := flattenedYahoo(team)
				key := yahooText(row, "team_key")
				if key != "" {
					output[key] = [3]int64{yahooInt(row, "waiver_priority"), yahooInt(row, "faab_balance"), yahooInt(row, "number_of_moves")}
					return
				}
			}
			for _, key := range sortedYahooKeys(value) {
				collect(value[key])
			}
		}
	}
	collect(value)
	return output
}

var _ YahooFantasySource = (*YahooPublicClient)(nil)
