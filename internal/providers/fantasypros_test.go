package providers

import (
	"strings"
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestFantasyProsFixtureParsesRanksAndYahooIdentities(t *testing.T) {
	rows, err := ParseFantasyProsECR(string(fixtureResponse(t, "testdata/fantasypros/ecr.html").Body))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	if rows[0].Name != "Ada Ace" || rows[0].Team != "NYY" || rows[0].YahooPlayerID == nil || *rows[0].YahooPlayerID != 9 || rows[0].Rank != 3 || rows[1].YahooPlayerID != nil || rows[1].Rank != 12 {
		t.Fatalf("rows=%#v", rows)
	}
}

func TestFantasyProsRejectsMalformedPagesAndEnforcesCoverage(t *testing.T) {
	for _, page := range []string{"<html>changed</html>", `<script>var ecrData = {"players":[}</script>`, `<script>var ecrData = {"players":[]};</script>`, `<script>var ecrData = {"players":[{"player_name":"","rank_ecr":1}]};</script>`} {
		if _, err := ParseFantasyProsECR(page); err == nil {
			t.Errorf("invalid page succeeded: %s", page)
		}
	}
	rows := make([]ECRRow, 100)
	if err := ValidateFantasyProsCompleteness(rows); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFantasyProsCompleteness(rows[:99]); err == nil || !strings.Contains(err.Error(), "100") {
		t.Fatalf("coverage error=%v", err)
	}
}

func TestFantasyProsClientUsesBoundedPublicPage(t *testing.T) {
	var captured transport.ValidatedRequest
	client := NewFantasyProsClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		captured = request
		return fixtureResponse(t, "testdata/fantasypros/ecr.html"), nil
	}}), ProductionFantasyProsEndpoints())
	rows, err := client.FetchECR()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	if captured.URL() != "https://www.fantasypros.com/mlb/rankings/overall.php" || captured.Timeout() != fantasyProsTimeout || captured.BodyLimit() != fantasyProsBodyLimit || len(captured.Headers()) != 0 {
		t.Fatalf("request=%#v", captured)
	}
}
