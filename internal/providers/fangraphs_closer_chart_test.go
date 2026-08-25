package providers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestFanGraphsCloserFixtureNormalizesRolesAndTeams(t *testing.T) {
	rows, err := ParseFanGraphsCloserChart(string(fixtureResponse(t, "testdata/fangraphs/closers.html").Body))
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	candidates := FanGraphsCloserCandidates(rows)
	if len(candidates) != 3 || candidates[0].Team != "CWS" || candidates[1].Team != "SF" || candidates[2].Team != "WSH" {
		t.Fatalf("candidates=%#v", candidates)
	}
	if _, err := ParseFanGraphsCloserChart("<html>changed</html>"); err == nil {
		t.Fatal("malformed chart succeeded")
	}
}

func TestFanGraphsCloserFetchBoundsAndCoverage(t *testing.T) {
	var captured transport.ValidatedRequest
	client := NewFanGraphsCloserClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		captured = request
		return fixtureResponse(t, "testdata/fangraphs/closers.html"), nil
	}}), ProductionFanGraphsCloserEndpoints())
	if _, err := client.FetchCloserChart(); err != nil {
		t.Fatal(err)
	}
	if captured.Timeout() != fanGraphsCloserTimeout || captured.BodyLimit() != fanGraphsCloserBodyLimit || len(captured.Headers()) != 2 {
		t.Fatalf("request=%#v", captured)
	}
	rows := make([]CloserChartEntry, 30)
	for index := range rows {
		rows[index] = CloserChartEntry{Team: fmt.Sprintf("T%02d", index), Name: "Closer", Role: "Closer"}
	}
	if err := ValidateFanGraphsCloserCoverage(rows); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFanGraphsCloserCoverage(rows[:29]); err == nil || !strings.Contains(err.Error(), "30") {
		t.Fatalf("coverage error=%v", err)
	}
}
