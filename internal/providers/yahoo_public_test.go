package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/transport"
)

func TestYahooRedzoneFixtureNormalizesCompleteLeagueAndAggregatesMatchup(t *testing.T) {
	requests := []transport.ValidatedRequest{}
	client := NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests = append(requests, request)
		return fixtureResponse(t, "testdata/yahoo/redzone-valid.json"), nil
	}}))
	feed, err := client.FetchRedzone("170874", "469.l.170874")
	if err != nil {
		t.Fatal(err)
	}
	if feed.League.LeagueKey != "469.l.170874" || feed.League.Name != "Yahoo Prize H2H-Cat 170874" || feed.League.Season != 2026 || feed.League.NumTeams != 2 || feed.League.ScoringType != domain.ScoringHeadToHead || feed.Week != 20 {
		t.Fatalf("league=%#v week=%d", feed.League, feed.Week)
	}
	if len(feed.Teams) != 2 || feed.Teams[0].Name != "New York Yankees" || feed.Teams[0].ManagerName != "--hidden--" || feed.Teams[1].ManagerName != "--hidden--" {
		t.Fatalf("teams=%#v", feed.Teams)
	}
	if len(feed.Players) != 5 || len(feed.Slots) != 5 {
		t.Fatalf("players=%d slots=%d", len(feed.Players), len(feed.Slots))
	}
	for _, player := range feed.Players {
		if player.YahooPlayerID == 64813 {
			t.Fatal("dropped player retained")
		}
	}
	if !reflect.DeepEqual(feed.RosterPositions, []RosterPosition{{Position: "C", Count: 1}, {Position: "1B", Count: 1}, {Position: "OF", Count: 2}, {Position: "SP", Count: 1}, {Position: "BN", Count: 1}, {Position: "IL", Count: 1}}) {
		t.Fatalf("roster positions=%#v", feed.RosterPositions)
	}
	if len(feed.Matchups) != 1 {
		t.Fatalf("matchups=%#v", feed.Matchups)
	}
	yankees := feed.Matchups[0].Teams[0]
	if yankees.Stats["7"] != "5" || yankees.Stats["3"] != "0.500" || yankees.Stats["H/AB"] != "10/20" || yankees.Stats["26"] != "3.00" || yankees.Stats["27"] != "1.33" || yankees.Stats["50"] != "6.0" || yankees.Wins != 8 || yankees.Losses != 2 {
		t.Fatalf("yankees matchup=%#v", yankees)
	}
	if _, found := yankees.Stats["6"]; found {
		t.Fatal("AVG building block leaked")
	}
	if _, found := yankees.Stats["60"]; found {
		t.Fatal("display-only stat leaked")
	}
	roster := feed.RosterWeekStats["469.l.170874.t.1"]
	if len(roster.Players) != 3 || roster.Players[0].HAB != "10-20" || roster.Players[1].InningsPitched != "6.0" {
		t.Fatalf("weekly roster=%#v", roster)
	}
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	target, _ := url.Parse(requests[0].URL())
	if target.Scheme != "https" || target.Host != "pub-api.fantasysports.yahoo.com" || target.Path != "/fantasy/v3/redzone/mlb" || target.Query().Get("league_id") != "170874" || target.Query().Get("format") != "json" {
		t.Fatalf("target=%s", requests[0].URL())
	}
	assertYahooPublicRequest(t, requests[0])
}

func TestYahooPublicSourceUsesExactCredentialFreePathsAndDormantDailyMethod(t *testing.T) {
	requests := []transport.ValidatedRequest{}
	executor := providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			return fixtureResponse(t, "testdata/yahoo/league-settings.json"), nil
		case 2:
			return fixtureResponse(t, "testdata/yahoo/standings.json"), nil
		case 3:
			return fixtureResponse(t, "testdata/yahoo/roster-team-1.json"), nil
		case 4:
			return fixtureResponse(t, "testdata/yahoo/roster-team-2.json"), nil
		case 5:
			return fixtureResponse(t, "testdata/yahoo/free-agents.json"), nil
		case 6:
			return transport.Response{Status: 200, Body: []byte(`{"data":[]}`)}, nil
		case 7:
			return fixtureResponse(t, "testdata/yahoo/matchup.json"), nil
		case 8, 9:
			return fixtureResponse(t, "testdata/yahoo/weekly-stats.json"), nil
		default:
			t.Fatalf("unexpected request %s", request.URL())
			return transport.Response{}, nil
		}
	}}
	client := NewProductionYahooPublicClient(transport.New(executor))
	if _, err := client.LeagueSettings("mlb.l.1"); err != nil {
		t.Fatal(err)
	}
	teams, err := client.Standings("mlb.l.1")
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{teams[0].TeamKey, teams[1].TeamKey}
	if _, err := client.LeagueRosters("mlb.l.1", keys); err != nil {
		t.Fatal(err)
	}
	if players, err := client.FreeAgents("mlb.l.1"); err != nil || len(players) != 2 {
		t.Fatalf("free agents=%#v err=%v", players, err)
	}
	week := 7
	if _, err := client.Scoreboard("mlb.l.1", &week); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RosterWeekStats("mlb.l.1.t.1", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RosterDayStats("mlb.l.1.t.1", 7, "2026-05-11"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.1/settings?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.1/standings?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/team/mlb.l.1.t.1/roster/players;out=ranks,percent_owned,percent_started?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/team/mlb.l.1.t.2/roster/players;out=ranks,percent_owned,percent_started?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.1/players;status=A;start=0;count=100;out=ranks,percent_owned,percent_started?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.1/players;status=A;start=100;count=100;out=ranks,percent_owned,percent_started?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/league/mlb.l.1/scoreboard;week=7?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/team/mlb.l.1.t.1/roster;week=7/players/stats;type=week;week=7?format=json",
		"https://pub-api-ro.fantasysports.yahoo.com/fantasy/v2/team/mlb.l.1.t.1/roster;date=2026-05-11/players/stats;type=date;date=2026-05-11?format=json",
	}
	if got := requestURLs(requests); !reflect.DeepEqual(got, want) {
		t.Fatalf("URLs=\n%q\nwant=\n%q", got, want)
	}
	for _, request := range requests {
		assertYahooPublicRequest(t, request)
	}
}

func TestYahooPublicRanksAndTransactionsRequireCompletePublicResponses(t *testing.T) {
	requests := 0
	client := NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests++
		if strings.Contains(request.URL(), "player_ids") {
			return fixtureResponse(t, "testdata/yahoo/public-ranks.json"), nil
		}
		return transport.Response{Status: 200, Body: []byte(`{"fantasy_content":{"league":[{}, {"standings":[{"teams":{"0":{"team":[[{"team_key":"mlb.l.1.t.1"},{"waiver_priority":1},{"faab_balance":"65"},{"number_of_moves":29}]]},"1":{"team":[[{"team_key":"mlb.l.1.t.2"},{"waiver_priority":2},{"faab_balance":"33"},{"number_of_moves":56}]]}}}]}]}}`)}, nil
	}}))
	players := make([]domain.FantasyPlayer, 51)
	for index := range players {
		players[index].YahooPlayerID = int64(index + 101)
	}
	if err := client.EnrichPlayerRanks(players); err != nil {
		t.Fatal(err)
	}
	if players[0].YahooRank == nil || *players[0].YahooRank != 216 || requests != 2 {
		t.Fatalf("players[0]=%#v requests=%d", players[0], requests)
	}
	teams := []domain.FantasyTeam{{TeamKey: "mlb.l.1.t.1"}, {TeamKey: "mlb.l.1.t.2"}}
	if err := client.EnrichTeamTransactions("mlb.l.1", teams); err != nil {
		t.Fatal(err)
	}
	if teams[0].FAABBalance != 65 || teams[1].Moves != 56 {
		t.Fatalf("teams=%#v", teams)
	}
}

func TestYahooPublicLeagueKeysEndpointsBoundsAndFailures(t *testing.T) {
	for input, want := range map[string][2]string{
		"170874":        {"170874", "mlb.l.170874"},
		"mlb.l.170874":  {"170874", "mlb.l.170874"},
		"469.l.170874":  {"170874", "469.l.170874"},
		"public.170874": {"170874", "mlb.l.170874"},
	} {
		leagueID, canonical := want[0], want[1]
		gotID, err := LeagueIDFromKey(input)
		if err != nil || gotID != leagueID {
			t.Errorf("LeagueIDFromKey(%q)=%q err=%v", input, gotID, err)
		}
		gotKey, err := CanonicalPublicLeagueKey(input)
		if err != nil || gotKey != canonical {
			t.Errorf("CanonicalPublicLeagueKey(%q)=%q err=%v", input, gotKey, err)
		}
	}
	for _, invalid := range []string{"", "garbage", "mlb.l.", "mlb.l.1/teams", "1.l.two"} {
		if _, err := LeagueIDFromKey(invalid); err == nil {
			t.Errorf("invalid key %q succeeded", invalid)
		}
	}
	if _, err := NewYahooEndpoints("https://evil.example/fantasy/v3/redzone/mlb", ProductionYahooEndpoints().Fantasy.String(), ProductionYahooEndpoints().PublicPlayers.String()); err == nil {
		t.Fatal("non-Yahoo HTTPS host succeeded")
	}
	client := NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 403}, nil
	}}))
	_, err := client.FetchRedzone("170874", "mlb.l.170874")
	var publicError *YahooPublicError
	if !errors.As(err, &publicError) || publicError.Kind != YahooBlockedError || publicError.Status != 403 {
		t.Fatalf("error=%v", err)
	}
	if _, err := client.RosterDayStats("mlb.l.1.t.1", 7, "2026-02-29"); err == nil {
		t.Fatal("invalid daily date dispatched")
	}
	for _, fixture := range []string{"redzone-malformed.json", "redzone-no-teams.json"} {
		fixture := fixture
		fixtureClient := NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
			return fixtureResponse(t, "testdata/yahoo/"+fixture), nil
		}}))
		if _, err := fixtureClient.FetchRedzone("170874", "mlb.l.170874"); err == nil {
			t.Errorf("%s succeeded", fixture)
		}
	}
}

func TestYahooFreeAgentPaginationDeduplicatesAndRejectsAnUnboundedCollection(t *testing.T) {
	page := func(start, count int) []byte {
		players := make([]map[string]any, count)
		for index := range players {
			players[index] = map[string]any{"player_id": start + index, "full": fmt.Sprintf("Player %d", start+index), "position": "OF"}
		}
		payload, _ := json.Marshal(map[string]any{"data": players})
		return payload
	}
	call := 0
	client := NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		call++
		switch call {
		case 1:
			return transport.Response{Status: 200, Body: page(1, 100)}, nil
		case 2:
			return transport.Response{Status: 200, Body: page(100, 100)}, nil
		default:
			return transport.Response{Status: 200, Body: []byte(`{"data":[]}`)}, nil
		}
	}}))
	players, err := client.FreeAgents("mlb.l.1")
	if err != nil || len(players) != 199 || players[0].YahooPlayerID != 1 || players[198].YahooPlayerID != 199 {
		t.Fatalf("players=%d err=%v", len(players), err)
	}
	call = 0
	client = NewProductionYahooPublicClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		call++
		return transport.Response{Status: 200, Body: page((call-1)*100+1, 100)}, nil
	}}))
	if _, err := client.FreeAgents("mlb.l.1"); err == nil || !strings.Contains(err.Error(), "exceeds 4,000") || call != 41 {
		t.Fatalf("error=%v calls=%d", err, call)
	}
}

func requestURLs(requests []transport.ValidatedRequest) []string {
	output := make([]string, len(requests))
	for index := range requests {
		output[index] = requests[index].URL()
	}
	return output
}

func assertYahooPublicRequest(t *testing.T, request transport.ValidatedRequest) {
	t.Helper()
	if request.Method() != transport.Get || request.Timeout() != yahooTimeout || request.BodyLimit() != yahooBodyLimit || len(request.Body()) != 0 {
		t.Errorf("request=%s method=%s bounds=%v/%d body=%d", request.URL(), request.Method(), request.Timeout(), request.BodyLimit(), len(request.Body()))
	}
	headers := request.Headers()
	if !reflect.DeepEqual(headers, []transport.Header{{Name: "Accept", Value: "application/json"}}) {
		t.Errorf("headers=%#v", headers)
	}
	for _, header := range headers {
		if strings.EqualFold(header.Name, "authorization") || strings.EqualFold(header.Name, "cookie") {
			t.Errorf("sensitive header=%s", header.Name)
		}
	}
}
