package providers

import (
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/transport"
)

func TestESPNFixturesDeduplicateEventsAndDegradeOddsPerGame(t *testing.T) {
	var requests []transport.ValidatedRequest
	executor := providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests = append(requests, request)
		if strings.Contains(request.URL(), "scoreboard") {
			return fixtureResponse(t, "testdata/espn/scoreboard-standard.json"), nil
		}
		return fixtureResponse(t, "testdata/espn/odds-standard.json"), nil
	}}
	endpoints, err := NewESPNEndpoints("https://site.api.espn.com/scoreboard", "https://sports.core.api.espn.com/leagues/mlb/")
	if err != nil {
		t.Fatal(err)
	}
	lines, err := NewESPNClient(transport.New(executor), endpoints, "0.2.0").FetchGameLines(time.Date(2026, 8, 15, 23, 0, 0, 0, time.UTC))
	if err != nil || len(lines.Games) != 2 || !lines.Games[0].Quoted || lines.Games[0].EventID != "event-1" {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
	for _, request := range requests {
		if request.Timeout() != espnTimeout || request.BodyLimit() != espnBodyLimit {
			t.Errorf("bounds=%v/%d", request.Timeout(), request.BodyLimit())
		}
		headers := request.Headers()
		if len(headers) != 2 || headers[0].Name == "" {
			t.Errorf("headers=%#v", headers)
		}
	}
	if !MatchesTeam("New York Yankees", "New-York Yankees") {
		t.Error("team folding failed")
	}
}

func TestESPNMalformedOddsBecomeBoundedIssue(t *testing.T) {
	executor := providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		if strings.Contains(request.URL(), "scoreboard") {
			return fixtureResponse(t, "testdata/espn/scoreboard-standard.json"), nil
		}
		return fixtureResponse(t, "testdata/espn/odds-malformed.json"), nil
	}}
	endpoints, _ := NewESPNEndpoints("https://site.api.espn.com/scoreboard", "https://sports.core.api.espn.com/leagues/mlb/")
	lines, err := NewESPNClient(transport.New(executor), endpoints, "0.2.0").FetchGameLines(time.Unix(2_000_000_000, 0))
	if err != nil || len(lines.Issues) != 2 || len(lines.Issues[0].Detail) > 256 {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
}

func TestESPNEmptyIncompleteMalformedAndZeroLineFixtures(t *testing.T) {
	for _, fixture := range []string{"scoreboard-empty.json", "scoreboard-incomplete.json"} {
		client := espnFixtureClient(t, fixture, "odds-empty.json", nil)
		lines, err := client.FetchGameLines(time.Unix(2_000_000_000, 0))
		if err != nil || len(lines.Games) != 0 {
			t.Errorf("%s lines=%#v err=%v", fixture, lines, err)
		}
	}
	if _, err := espnFixtureClient(t, "scoreboard-malformed.json", "odds-empty.json", nil).FetchGameLines(time.Unix(2_000_000_000, 0)); err == nil {
		t.Fatal("malformed scoreboard succeeded")
	}
	for _, fixture := range []string{"odds-empty.json", "odds-zero-moneyline.json"} {
		lines, err := espnFixtureClient(t, "scoreboard-standard.json", fixture, nil).FetchGameLines(time.Unix(2_000_000_000, 0))
		if err != nil || len(lines.Games) != 2 || lines.Games[0].Quoted {
			t.Errorf("%s lines=%#v err=%v", fixture, lines, err)
		}
	}
}

func TestESPNIdentifiersAreEscapedAsPathSegments(t *testing.T) {
	scoreboard := []byte(`{"events":[{"id":"event/one","competitions":[{"id":"competition?one","competitors":[{"homeAway":"away","team":{"displayName":"Away"}},{"homeAway":"home","team":{"displayName":"Home"}}]}]}]}`)
	var oddsURL string
	client := espnFixtureClient(t, "", "odds-empty.json", func(request transport.ValidatedRequest) *transport.Response {
		if strings.Contains(request.URL(), "scoreboard") {
			response := transport.Response{Status: 200, Body: scoreboard}
			return &response
		}
		oddsURL = request.URL()
		return nil
	})
	if _, err := client.FetchGameLines(time.Unix(2_000_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(oddsURL, "event%2Fone") || !strings.Contains(oddsURL, "competition%3Fone") {
		t.Fatalf("identifiers were not escaped: %s", oddsURL)
	}
}

func TestESPNFirstEventIdentityWinsEvenWhenIncomplete(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"events":[{"id":"same-event","competitions":[]}]}`),
		[]byte(`{"events":[{"id":"same-event","competitions":[{"id":"competition","competitors":[{"homeAway":"away","team":{"displayName":"Away"}},{"homeAway":"home","team":{"displayName":"Home"}}]}]}]}`),
	}
	requestCount := 0
	executor := providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		response := transport.Response{Status: 200, Body: responses[requestCount]}
		requestCount++
		return response, nil
	}}
	endpoints, err := NewESPNEndpoints("https://site.api.espn.com/scoreboard", "https://sports.core.api.espn.com/leagues/mlb/")
	if err != nil {
		t.Fatal(err)
	}
	lines, err := NewESPNClient(transport.New(executor), endpoints, "0.2.0").FetchGameLines(time.Unix(2_000_000_000, 0))
	if err != nil || len(lines.Games) != 0 || requestCount != 2 {
		t.Fatalf("lines=%#v requests=%d err=%v", lines, requestCount, err)
	}
}

func espnFixtureClient(t *testing.T, scoreboardFixture, oddsFixture string, override func(transport.ValidatedRequest) *transport.Response) *ESPNClient {
	t.Helper()
	executor := providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		if override != nil {
			if response := override(request); response != nil {
				return *response, nil
			}
		}
		if strings.Contains(request.URL(), "scoreboard") {
			return fixtureResponse(t, "testdata/espn/"+scoreboardFixture), nil
		}
		return fixtureResponse(t, "testdata/espn/"+oddsFixture), nil
	}}
	endpoints, err := NewESPNEndpoints("https://site.api.espn.com/scoreboard", "https://sports.core.api.espn.com/leagues/mlb/")
	if err != nil {
		t.Fatal(err)
	}
	return NewESPNClient(transport.New(executor), endpoints, "0.2.0")
}
