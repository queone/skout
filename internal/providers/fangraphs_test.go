package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestFanGraphsFixturesNormalizeIDsFieldsAndCrosswalk(t *testing.T) {
	leaders, err := ParseFanGraphsLeaderboard(fixtureResponse(t, "testdata/fangraphs/leaderboard.json").Body)
	if err != nil || len(leaders) != 2 || leaders[0].FanGraphsID != "101" || leaders[1].FanGraphsID != "sa3020134" || leaders[0].MLBAMID == nil || *leaders[0].MLBAMID != 700001 {
		t.Fatalf("leaders=%#v err=%v", leaders, err)
	}
	rows, err := ParseFanGraphsProjections(fixtureResponse(t, "testdata/fangraphs/projections.json").Body)
	if err != nil || len(rows) != 2 || rows[0].PA != 620 || rows[1].IP != 182 {
		t.Fatalf("projections=%#v err=%v", rows, err)
	}
	resolved := ResolveFanGraphsMLBAMID(rows[1].MLBAMID, rows[1].FanGraphsID, map[string]int64{"sa3020134": 700002})
	if resolved == nil || *resolved != 700002 {
		t.Fatalf("resolved=%v", resolved)
	}
	if own := ResolveFanGraphsMLBAMID(rows[0].MLBAMID, "ignored", map[string]int64{"ignored": 9}); own == nil || *own != 700001 {
		t.Fatalf("own id=%v", own)
	}
}

func TestFanGraphsClientBuildsExactQueriesAndRejectsInvalidShapes(t *testing.T) {
	var requests []transport.ValidatedRequest
	client := NewFanGraphsClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requests = append(requests, request)
		if strings.Contains(request.URL(), "/leaders/") {
			return fixtureResponse(t, "testdata/fangraphs/leaderboard.json"), nil
		}
		return fixtureResponse(t, "testdata/fangraphs/projections.json"), nil
	}}), ProductionFanGraphsEndpoints())
	if _, err := client.FetchLeaderboard(2026); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchProjections(2026, "steamer", "pitching"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	leaderURL, _ := url.Parse(requests[0].URL())
	projectionURL, _ := url.Parse(requests[1].URL())
	if leaderURL.Query().Get("pageItems") != "2000" || projectionURL.Query().Get("type") != "steamer" || projectionURL.Query().Get("stats") != "pit" || requests[1].Timeout() != fanGraphsTimeout || requests[1].BodyLimit() != fanGraphsBodyLimit {
		t.Fatalf("leader=%s projection=%s", leaderURL, projectionURL)
	}
	if _, err := client.FetchProjections(2026, "unknown", "batting"); err == nil {
		t.Fatal("unknown system accepted")
	}
	for _, payload := range [][]byte{[]byte("{"), []byte(`{"data":{}}`), []byte(`{"other":[]}`)} {
		if _, err := ParseFanGraphsLeaderboard(payload); err == nil {
			t.Errorf("invalid response succeeded: %s", payload)
		}
	}
}

func TestFanGraphsSnapshotEnforcesCoverageAndDeterministicResolution(t *testing.T) {
	leaders := make([]map[string]any, 0, 100)
	for id := 1; id <= 100; id++ {
		leaders = append(leaders, map[string]any{"playerid": fmt.Sprintf("fg%d", id), "xMLBAMID": 700000 + id, "FB%": .4, "HR/FB": .1})
	}
	projections := make([]map[string]any, 0, 17)
	for id := 1; id <= 17; id++ {
		projections = append(projections, map[string]any{"playerid": fmt.Sprintf("fg%d", id), "PA": 500 + id, "HR": id})
	}
	leaderPayload, _ := json.Marshal(map[string]any{"data": leaders})
	projectionPayload, _ := json.Marshal(projections)
	client := NewFanGraphsClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		if strings.Contains(request.URL(), "/leaders/") {
			return transport.Response{Status: 200, Body: leaderPayload}, nil
		}
		return transport.Response{Status: 200, Body: projectionPayload}, nil
	}}), ProductionFanGraphsEndpoints())
	snapshot, err := client.FetchSnapshot(2026)
	if err != nil || len(snapshot.BattedBall) != 100 || len(snapshot.Projections) != 102 {
		t.Fatalf("snapshot=%d/%d err=%v", len(snapshot.BattedBall), len(snapshot.Projections), err)
	}
	if snapshot.Projections[0].MLBAMID != 700001 || snapshot.Projections[0].Source != "steamer" || snapshot.Projections[0].StatGroup != "batting" {
		t.Fatalf("first projection=%#v", snapshot.Projections[0])
	}
	short := NewFanGraphsClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 200, Body: []byte(`[]`)}, nil
	}}), ProductionFanGraphsEndpoints())
	if _, err := short.FetchSnapshot(2026); err == nil || !strings.Contains(err.Error(), "fewer than 100") {
		t.Fatalf("short snapshot error=%v", err)
	}
}
