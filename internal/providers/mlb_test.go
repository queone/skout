package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/transport"
)

type providerExecutor struct {
	execute func(transport.ValidatedRequest) (transport.Response, error)
}

func (executor providerExecutor) Execute(request transport.ValidatedRequest) (transport.Response, error) {
	return executor.execute(request)
}

func fixtureResponse(t *testing.T, path string) transport.Response {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return transport.Response{Status: 200, Body: data}
}

func TestMLBFantasyViewsUseBoundedExactRangeLogAndBoxscoreRequests(t *testing.T) {
	tests := []struct {
		name, fixture, path, query string
		run                        func(*MLBClient) error
	}{
		{name: "HittingRange", fixture: "testdata/mlb/bulk-hitting.json", path: "/api/v1/stats", query: "group=hitting", run: func(client *MLBClient) error {
			rows, err := client.FetchHittingStatsByDateRange(2026, "2026-04-01", "2026-04-01")
			if err == nil && len(rows) == 0 {
				return errors.New("empty hitting range")
			}
			return err
		}},
		{name: "PitchingRange", fixture: "testdata/mlb/bulk-pitching.json", path: "/api/v1/stats", query: "group=pitching", run: func(client *MLBClient) error {
			rows, err := client.FetchPitchingStatsByDateRange(2026, "2026-04-01", "2026-04-02")
			if err == nil && len(rows) == 0 {
				return errors.New("empty pitching range")
			}
			return err
		}},
		{name: "HitterLog", fixture: "testdata/mlb/hitter-game-log.json", path: "/api/v1/people/700001/stats", query: "group=hitting", run: func(client *MLBClient) error {
			rows, err := client.FetchHitterGameLog(700001, 2026)
			if err == nil && (len(rows) != 1 || rows[0].GameID != 800010) {
				return fmt.Errorf("hitter rows=%#v", rows)
			}
			return err
		}},
		{name: "PitcherLog", fixture: "testdata/mlb/pitcher-game-log.json", path: "/api/v1/people/600002/stats", query: "group=pitching", run: func(client *MLBClient) error {
			rows, err := client.FetchPitcherGameLog(600002, 2026)
			if err == nil && len(rows) != 2 {
				return fmt.Errorf("pitcher rows=%#v", rows)
			}
			return err
		}},
		{name: "Boxscore", fixture: "testdata/mlb/boxscore.json", path: "/api/v1/game/800010/boxscore", run: func(client *MLBClient) error {
			boxscore, err := client.FetchBoxscore(800010)
			if err == nil && (len(boxscore.Away.BattingOrder) != 1 || boxscore.Away.Players[700001].Batting == nil || boxscore.Home.Players[600002].Pitching == nil) {
				return fmt.Errorf("boxscore=%#v", boxscore)
			}
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewProductionMLBClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
				target, err := url.Parse(request.URL())
				if err != nil {
					t.Fatal(err)
				}
				if target.Path != test.path || test.query != "" && !strings.Contains(target.RawQuery, test.query) {
					t.Fatalf("request=%s", request.URL())
				}
				if strings.Contains(test.name, "Range") && (!strings.Contains(target.RawQuery, "stats=byDateRange") || !strings.Contains(target.RawQuery, "gameType=R") || !strings.Contains(target.RawQuery, "limit=2000") || !strings.Contains(target.RawQuery, "startDate=2026-04-01")) {
					t.Fatalf("range query=%s", target.RawQuery)
				}
				return fixtureResponse(t, test.fixture), nil
			}}))
			if err := test.run(client); err != nil {
				t.Fatal(err)
			}
		})
	}
	client := NewProductionMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 200, Body: []byte(`{}`)}, nil
	}}))
	if _, err := client.FetchHittingStatsByDateRange(2026, "2026-04-02", "2026-04-01"); err == nil {
		t.Fatal("reversed range accepted")
	}
	if _, err := client.FetchBoxscore(1); err == nil || !strings.Contains(err.Error(), "teams envelope") {
		t.Fatalf("incomplete boxscore error=%v", err)
	}
	malformed := func(payload string) *MLBClient {
		return NewProductionMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
			return transport.Response{Status: 200, Body: []byte(payload)}, nil
		}}))
	}
	if _, err := malformed(`{"stats":[{"splits":[{"player":{}}]}]}`).FetchHittingStatsByDateRange(2026, "2026-04-01", "2026-04-01"); err == nil || !strings.Contains(err.Error(), "player identity") {
		t.Fatalf("incomplete hitting split error=%v", err)
	}
	if _, err := malformed(`{}`).FetchHitterGameLog(1, 2026); err == nil || !strings.Contains(err.Error(), "stats envelope") {
		t.Fatalf("incomplete hitter log error=%v", err)
	}
	if _, err := malformed(`{"teams":{"away":{},"home":{}}}`).FetchBoxscore(1); err == nil || !strings.Contains(err.Error(), "player maps") {
		t.Fatalf("incomplete boxscore teams error=%v", err)
	}
}

func TestMLBFixturesDecodeThroughExactBoundedEndpoints(t *testing.T) {
	requests := []transport.ValidatedRequest{}
	client := NewMLBClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests = append(requests, request)
		target, _ := url.Parse(request.URL())
		fixture := ""
		switch {
		case strings.HasSuffix(target.Path, "/teams"):
			fixture = "testdata/mlb/team-directory.json"
		case strings.HasSuffix(target.Path, "/standings"):
			fixture = "testdata/mlb/standings.json"
		case strings.Contains(target.Path, "/roster"):
			fixture = "testdata/mlb/roster.json"
		case strings.HasSuffix(target.Path, "/schedule"):
			fixture = "testdata/mlb/schedule.json"
		case strings.HasSuffix(target.Path, "/stats") && target.Query().Get("group") == "hitting":
			fixture = "testdata/mlb/bulk-hitting.json"
		case strings.HasSuffix(target.Path, "/stats"):
			fixture = "testdata/mlb/bulk-pitching.json"
		default:
			t.Fatalf("unexpected request %s", request.URL())
		}
		return fixtureResponse(t, fixture), nil
	}}), ProductionMLBEndpoints())
	teams, err := client.FetchTeamDirectory(2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 30 || teams[0] != (TeamDirectoryEntry{TeamID: 133, Name: "Athletics", LocationName: "Athletics", ClubName: "Athletics", Abbreviation: "ATH", LeagueID: 103}) || teams[29].Abbreviation != "WSH" {
		t.Fatalf("teams=%#v", teams)
	}
	var yankees TeamDirectoryEntry
	for _, team := range teams {
		if team.TeamID == 147 {
			yankees = team
		}
	}
	if yankees != (TeamDirectoryEntry{TeamID: 147, Name: "New York Yankees", LocationName: "New York", ClubName: "Yankees", Abbreviation: "NYY", LeagueID: 103}) {
		t.Fatalf("yankees=%#v", yankees)
	}
	standings, err := client.FetchStandings(2026)
	if err != nil || !reflect.DeepEqual(standings, []TeamStanding{{TeamID: 147, LeagueID: 103, Wins: 30, Losses: 20, GamesBack: "—"}, {TeamID: 119, LeagueID: 104, Wins: 31, Losses: 19, GamesBack: "—"}}) {
		t.Fatalf("standings=%#v err=%v", standings, err)
	}
	roster, err := client.FetchRoster(108)
	wantRoster := []RosterMember{{PersonID: 660271, FullName: "Two Way", Position: "TWP", PrimaryType: "H", Status: "A", JerseyNumber: "17"}, {PersonID: 660271, FullName: "Two Way", Position: "TWP", PrimaryType: "P", Status: "A", JerseyNumber: "17"}, {PersonID: 600010, FullName: "Starter", Position: "SP", PrimaryType: "P", Status: "A", JerseyNumber: "45"}, {PersonID: 700010, FullName: "Fielder", Position: "SS", PrimaryType: "H", Status: "MIN"}}
	if err != nil || !reflect.DeepEqual(roster, wantRoster) {
		t.Fatalf("roster=%#v err=%v", roster, err)
	}
	games, err := client.FetchSchedule("2026-05-15")
	awayPitcher, homePitcher, fifth, ninth := int64(600001), int64(600002), int64(5), int64(9)
	wantGames := []ScheduleGame{
		{GameID: 800001, GameDate: "2026-05-15T23:05:00Z", DetailedState: "In Progress", AwayTeamID: 110, AwayTeamName: "Baltimore Orioles", HomeTeamID: 147, HomeTeamName: "New York Yankees", AwayProbablePitcherID: &awayPitcher, AwayProbablePitcherName: "Away Starter", HomeProbablePitcherID: &homePitcher, HomeProbablePitcherName: "Home Starter", Linescore: &Linescore{Inning: &fifth, InningOrdinal: "5th", InningState: "Top", AwayRuns: 2, HomeRuns: 1}, AwayLineup: []LineupPlayer{{PersonID: 700001, FullName: "Away Hitter"}}, HomeLineup: []LineupPlayer{{PersonID: 700002, FullName: "Home Hitter"}}},
		{GameID: 800002, GameDate: "2026-05-15T20:00:00Z", DetailedState: "Scheduled", AwayTeamID: 111, AwayTeamName: "Boston Red Sox", HomeTeamID: 141, HomeTeamName: "Toronto Blue Jays"},
		{GameID: 800004, GameDate: "2026-05-15T17:00:00Z", DetailedState: "Final", AwayTeamID: 119, AwayTeamName: "Los Angeles Dodgers", HomeTeamID: 135, HomeTeamName: "San Diego Padres", Linescore: &Linescore{Inning: &ninth, InningOrdinal: "9th", InningState: "End", AwayRuns: 4, HomeRuns: 3}},
	}
	if err != nil || !reflect.DeepEqual(games, wantGames) {
		t.Fatalf("games=%#v err=%v", games, err)
	}
	hitting, err := client.FetchBulkHittingStats(2026, "R")
	wantHitting := BulkHittingSplit{Player: BulkPlayer{PersonID: 700001, FullName: "Bulk Hitter"}, Team: BulkTeam{TeamID: 147}, Position: BulkPosition{PositionType: "Fielder"}, Stat: HittingStats{GamesPlayed: 5, PlateAppearances: 22, AtBats: 20, Hits: 8, HomeRuns: 2, RBI: 6, Runs: 5, StolenBases: 1, Average: ".400", OnBasePercentage: ".455", SluggingPercentage: ".750", OPS: "1.205", Strikeouts: 3, Walks: 2, Doubles: 2, TotalBases: 15, GroundedIntoDP: 1, BABIP: ".429"}}
	if err != nil || !reflect.DeepEqual(hitting, []BulkHittingSplit{wantHitting}) {
		t.Fatalf("hitting=%#v err=%v", hitting, err)
	}
	pitching, err := client.FetchBulkPitchingStats(2026, "R")
	wantPitching := BulkPitchingSplit{Player: BulkPlayer{PersonID: 600001, FullName: "Bulk Pitcher"}, Team: BulkTeam{TeamID: 110}, Position: BulkPosition{PositionType: "Pitcher"}, Stat: PitchingStats{GamesPitched: 3, GamesStarted: 2, InningsPitched: "12.1", Wins: 1, Strikeouts: 15, Walks: 3, ERA: "1.46", WHIP: "0.89", Runs: 3, HitsAllowed: 8, EarnedRuns: 2, HomeRunsAllowed: 1, WildPitches: 1, BattersFaced: 47, GamesFinished: 1, StrikeoutsPerNine: "10.95", WalksPerNine: "2.19", HitsPerNine: "5.84", HomeRunsPerNine: "0.73", StrikeoutWalkRatio: "5.00", Pickoffs: 1, StolenBasesAllowed: 1, NumberOfPitches: 190, PitchesPerInning: "15.41"}}
	if err != nil || !reflect.DeepEqual(pitching, []BulkPitchingSplit{wantPitching}) {
		t.Fatalf("pitching=%#v err=%v", pitching, err)
	}
	wantRequests := []struct {
		path  string
		query url.Values
	}{
		{"/api/v1/teams", url.Values{"season": {"2026"}, "sportId": {"1"}}},
		{"/api/v1/standings", url.Values{"leagueId": {"103,104"}, "season": {"2026"}}},
		{"/api/v1/teams/108/roster", url.Values{"rosterType": {"40Man"}}},
		{"/api/v1/schedule", url.Values{"date": {"2026-05-15"}, "hydrate": {"linescore,probablePitcher,lineups"}, "sportId": {"1"}}},
		{"/api/v1/stats", url.Values{"gameType": {"R"}, "group": {"hitting"}, "limit": {"2000"}, "playerPool": {"All"}, "season": {"2026"}, "stats": {"season"}}},
		{"/api/v1/stats", url.Values{"gameType": {"R"}, "group": {"pitching"}, "limit": {"2000"}, "playerPool": {"All"}, "season": {"2026"}, "stats": {"season"}}},
	}
	if len(requests) != len(wantRequests) {
		t.Fatalf("request count=%d want=%d", len(requests), len(wantRequests))
	}
	for index, request := range requests {
		target, err := url.Parse(request.URL())
		if err != nil {
			t.Fatal(err)
		}
		if request.Method() != transport.Get || target.Scheme != "https" || target.Host != "statsapi.mlb.com" || target.Path != wantRequests[index].path || !reflect.DeepEqual(target.Query(), wantRequests[index].query) || len(request.Headers()) != 0 || len(request.Body()) != 0 || request.Timeout() != mlbTimeout || request.BodyLimit() != mlbBodyLimit {
			t.Errorf("request[%d]=%s headers=%#v bounds=%v/%d", index, request.URL(), request.Headers(), request.Timeout(), request.BodyLimit())
		}
	}
}

type providerClock struct{ value time.Time }

func (clock providerClock) Now() time.Time { return clock.value }

func TestMLBScheduleCacheClassifiesPayloadDecodeFailuresAsCorrupt(t *testing.T) {
	payload, err := os.ReadFile("testdata/mlb/schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	storedAt := time.Unix(2_000_000_000, 0)
	for _, test := range []struct {
		name      string
		cached    []byte
		readAt    time.Time
		wantState cache.State
		wantCalls int
	}{
		{name: "fresh valid", cached: payload, readAt: storedAt, wantState: cache.Hit, wantCalls: 0},
		{name: "expired valid", cached: payload, readAt: storedAt.Add(ScheduleTTL), wantState: cache.Expired, wantCalls: 1},
		{name: "fresh invalid", cached: []byte("not json"), readAt: storedAt, wantState: cache.Corrupt, wantCalls: 1},
		{name: "expired invalid", cached: []byte("not json"), readAt: storedAt.Add(ScheduleTTL), wantState: cache.Corrupt, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "cache")
			if err := cache.WithClock(root, providerClock{storedAt}).Put("mlb", "schedule-2026-05-15", test.cached); err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := NewMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
				calls++
				return transport.Response{Status: 200, Body: payload}, nil
			}}), ProductionMLBEndpoints())
			result, err := client.FetchScheduleCached("2026-05-15", cache.WithClock(root, providerClock{test.readAt}))
			if err != nil {
				t.Fatal(err)
			}
			if result.CacheState != test.wantState || calls != test.wantCalls || len(result.Games) != 3 {
				t.Fatalf("result=%#v calls=%d", result, calls)
			}
		})
	}
}

func TestMLBDirectoryRejectsDuplicateIdentityBeforeDeduplication(t *testing.T) {
	payload, err := os.ReadFile("testdata/mlb/team-directory.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Teams []json.RawMessage `json:"teams"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	document.Teams = append(document.Teams, document.Teams[0])
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	client := NewMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 200, Body: payload}, nil
	}}), ProductionMLBEndpoints())
	_, err = client.FetchTeamDirectory(2026)
	if err == nil || !strings.Contains(err.Error(), "received 31 rows and 30 unique identities") {
		t.Fatalf("error=%v", err)
	}
}

func TestMLBScheduleAcceptsFrozenNameAliasesAndPreservesLineupPresence(t *testing.T) {
	payload := []byte(`{"dates":[{"games":[{"gamePk":1,"gameDate":"2026-05-15T12:00:00Z","teams":{"away":{"team":{"id":10,"fullName":"Away Full"},"probablePitcher":{"id":20,"name":"Away Named"}},"home":{"team":{"id":11,"name":"Home Named"},"probablePitcher":{"id":21,"fullName":"Home Full"}}},"lineups":{"awayPlayers":[],"homePlayers":[{"id":0,"fullName":"Invalid"}]}},{"gamePk":2,"teams":{"away":{"team":{"id":12}},"home":{"team":{"id":13}}}}]}]}`)
	games, err := decodeSchedule(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 || games[0].AwayTeamName != "Away Full" || games[0].HomeTeamName != "Home Named" || games[0].AwayProbablePitcherName != "Away Named" || games[0].HomeProbablePitcherName != "Home Full" {
		t.Fatalf("games=%#v", games)
	}
	if games[0].AwayLineup == nil || len(games[0].AwayLineup) != 0 || games[0].HomeLineup == nil || len(games[0].HomeLineup) != 0 {
		t.Fatalf("present lineups=%#v/%#v", games[0].AwayLineup, games[0].HomeLineup)
	}
	if games[1].AwayLineup != nil || games[1].HomeLineup != nil {
		t.Fatalf("absent lineups=%#v/%#v", games[1].AwayLineup, games[1].HomeLineup)
	}
}

func TestMLBValidationAndRequiredEnvelopesFailBeforeOrAfterDispatch(t *testing.T) {
	calls := 0
	client := NewMLBClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		calls++
		return transport.Response{Status: 200, Body: []byte(`{}`)}, nil
	}}), ProductionMLBEndpoints())
	validations := []struct {
		name string
		run  func() error
	}{
		{"season", func() error { _, err := client.FetchTeamDirectory(0); return err }},
		{"team ID", func() error { _, err := client.FetchRoster(0); return err }},
		{"date", func() error { _, err := client.FetchSchedule("not-a-date"); return err }},
		{"bulk season", func() error { _, err := client.FetchBulkHittingStats(10, "R"); return err }},
		{"game type", func() error { _, err := client.FetchBulkPitchingStats(2026, "X"); return err }},
	}
	for _, test := range validations {
		before := calls
		if err := test.run(); err == nil || calls != before {
			t.Errorf("%s validation err=%v calls=%d before=%d", test.name, err, calls, before)
		}
	}
	envelopes := []struct {
		name string
		run  func() error
	}{
		{"directory", func() error { _, err := client.FetchTeamDirectory(2026); return err }},
		{"standings", func() error { _, err := client.FetchStandings(2026); return err }},
		{"roster", func() error { _, err := client.FetchRoster(108); return err }},
		{"schedule", func() error { _, err := client.FetchSchedule("2026-05-15"); return err }},
	}
	for _, test := range envelopes {
		if err := test.run(); err == nil || !strings.Contains(err.Error(), "envelope") {
			t.Errorf("%s envelope error=%v", test.name, err)
		}
	}
	for _, root := range []string{"http://example.com", "ftp://statsapi.mlb.com", "https://user:secret@statsapi.mlb.com", "https://statsapi.mlb.com?x=1", "https://statsapi.mlb.com#fragment", "/api/v1"} {
		if _, err := NewMLBEndpoints(root); err == nil {
			t.Errorf("invalid endpoint %q accepted", root)
		}
	}
	if endpoint, err := NewMLBEndpoints("http://127.0.0.1:8080/api/v1"); err != nil || endpoint.Root.String() != "http://127.0.0.1:8080/api/v1/" {
		t.Fatalf("loopback endpoint=%v err=%v", endpoint.Root, err)
	}
}

func TestMLBProviderFailuresCarryOperationContext(t *testing.T) {
	for _, test := range []struct {
		name     string
		response transport.Response
		err      error
		want     string
	}{
		{name: "transport", err: errors.New("network unavailable"), want: "fetch MLB standings: request failed"},
		{name: "status", response: transport.Response{Status: 503}, want: "fetch MLB standings: provider returned HTTP 503"},
		{name: "json", response: transport.Response{Status: 200, Body: []byte("not json")}, want: "fetch MLB standings: decode JSON response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
				return test.response, test.err
			}}), ProductionMLBEndpoints())
			_, err := client.FetchStandings(2026)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "retry") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestMLBPitcherGameLogUsesExactBoundedRequestAndNormalizesRows(t *testing.T) {
	var request transport.ValidatedRequest
	client := NewMLBClient(transport.New(providerExecutor{execute: func(value transport.ValidatedRequest) (transport.Response, error) {
		request = value
		return transport.Response{Status: 200, Body: []byte(`{"stats":[{"splits":[{"date":"2026-04-01","game":{"gamePk":7},"isHome":true,"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.1","earnedRuns":2}}]}]}`)}, nil
	}}), ProductionMLBEndpoints())
	rows, err := client.FetchPitcherGameLog(600001, 2026)
	if err != nil {
		t.Fatal(err)
	}
	want := []PitchingGameLogEntry{{Date: "2026-04-01", GameID: 7, IsHome: true, OpponentTeamID: 147, Stat: PitchingStats{GamesStarted: 1, InningsPitched: "6.1", EarnedRuns: 2}}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows=%#v", rows)
	}
	target, err := url.Parse(request.URL())
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := url.Values{"group": {"pitching"}, "season": {"2026"}, "stats": {"gameLog"}}
	if request.Method() != transport.Get || target.Path != "/api/v1/people/600001/stats" || !reflect.DeepEqual(target.Query(), wantQuery) || request.Timeout() != mlbTimeout || request.BodyLimit() != mlbBodyLimit {
		t.Fatalf("request=%s method=%s bounds=%v/%d", request.URL(), request.Method(), request.Timeout(), request.BodyLimit())
	}

	emptyClient := NewMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 200, Body: []byte(`{}`)}, nil
	}}), ProductionMLBEndpoints())
	empty, err := emptyClient.FetchPitcherGameLog(600001, 2026)
	if err == nil || len(empty) != 0 || !strings.Contains(err.Error(), "stats envelope") {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
}

type qualityStartExecutor struct {
	mu       sync.Mutex
	requests []transport.ValidatedRequest
	active   atomic.Int64
	maximum  atomic.Int64
}

func (executor *qualityStartExecutor) Execute(request transport.ValidatedRequest) (response transport.Response, err error) {
	target, err := url.Parse(request.URL())
	if err != nil {
		return transport.Response{}, err
	}
	segments := strings.Split(strings.Trim(target.Path, "/"), "/")
	if len(segments) < 3 {
		return transport.Response{Status: 404}, nil
	}
	personID, err := strconv.ParseInt(segments[len(segments)-2], 10, 64)
	if err != nil {
		return transport.Response{}, err
	}
	executor.mu.Lock()
	executor.requests = append(executor.requests, request)
	executor.mu.Unlock()
	active := executor.active.Add(1)
	defer executor.active.Add(-1)
	for {
		maximum := executor.maximum.Load()
		if active <= maximum || executor.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	if personID == 3 {
		return transport.Response{Status: 503}, nil
	}
	if personID == 6 {
		panic("simulated worker failure")
	}
	return transport.Response{Status: 200, Body: []byte(`{"stats":[{"splits":[
{"date":"2026-03-31","game":{"gamePk":1},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.0","earnedRuns":3}},
{"date":"2026-04-01","game":{"gamePk":2},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.0","earnedRuns":3}},
{"date":"2026-04-02","game":{"gamePk":3},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"5.2","earnedRuns":0}},
{"date":"2026-04-03","game":{"gamePk":4},"opponent":{"id":147},"stat":{"gamesStarted":0,"inningsPitched":"9.0","earnedRuns":0}},
{"date":"2026-04-04","game":{"gamePk":5},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.0","earnedRuns":4}},
{"date":"2026-04-05","game":{"gamePk":6},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.3","earnedRuns":0}},
{"date":"2026-05-31","game":{"gamePk":7},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"6.1","earnedRuns":3}},
{"date":"2026-06-01","game":{"gamePk":8},"opponent":{"id":147},"stat":{"gamesStarted":1,"inningsPitched":"7.0","earnedRuns":1}}
]}]}`)}, nil
}

func TestMLBQualityStartsAreInclusiveDeduplicatedBoundedAndPartial(t *testing.T) {
	executor := &qualityStartExecutor{}
	client := NewMLBClient(transport.New(executor), ProductionMLBEndpoints())
	result, err := client.FetchQualityStartsByDateRange(2026, "2026-04-01", "2026-05-31", []int64{1, 2, 3, 4, 5, 6, 7, 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, personID := range []int64{1, 2, 4, 5, 7} {
		if result.Counts[personID] != 2 {
			t.Errorf("person %d count=%d", personID, result.Counts[personID])
		}
	}
	if len(result.Counts) != 5 || len(result.Issues) != 2 || result.Issues[0].PersonID != 3 || result.Issues[1].PersonID != 6 {
		t.Fatalf("result=%#v", result)
	}
	if !strings.Contains(result.Issues[0].Detail, "HTTP 503") || !strings.Contains(result.Issues[1].Detail, "did not complete normally") {
		t.Fatalf("issues=%#v", result.Issues)
	}
	for _, issue := range result.Issues {
		if len([]rune(issue.Detail)) > 256 {
			t.Errorf("unbounded issue=%q", issue.Detail)
		}
	}
	if executor.maximum.Load() > 5 {
		t.Fatalf("maximum concurrency=%d", executor.maximum.Load())
	}
	executor.mu.Lock()
	requestCount := len(executor.requests)
	executor.mu.Unlock()
	if requestCount != 7 {
		t.Fatalf("requests=%d", requestCount)
	}
}

func TestMLBQualityStartsValidateBeforeDispatchAndOmitZeroCounts(t *testing.T) {
	requests := atomic.Int64{}
	client := NewMLBClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		requests.Add(1)
		return transport.Response{Status: 200, Body: []byte(`{"stats":[{"splits":[]}]}`)}, nil
	}}), ProductionMLBEndpoints())
	for _, run := range []func() error{
		func() error { _, err := client.FetchQualityStarts(2026, []int64{1, 0}); return err },
		func() error {
			_, err := client.FetchQualityStartsByDateRange(2026, "2026-04-02", "2026-04-01", []int64{1})
			return err
		},
		func() error {
			_, err := client.FetchQualityStartsByDateRange(2026, "2026-02-30", "2026-03-01", []int64{1})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("invalid quality-start request accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("validation dispatched %d requests", requests.Load())
	}
	result, err := client.FetchQualityStarts(2026, []int64{1})
	if err != nil || len(result.Counts) != 0 || len(result.Issues) != 0 || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}
