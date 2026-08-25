package providers

import (
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestOddsSharkFixtureShapeHeadersAndValidation(t *testing.T) {
	var captured transport.ValidatedRequest
	requestCount := 0
	client := NewOddsSharkClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		requestCount++
		captured = request
		return fixtureResponse(t, "testdata/oddsshark/slate.json"), nil
	}}), ProductionOddsSharkEndpoints())
	lines, err := client.FetchGameLines("2026-08-16")
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
	if captured.URL() != "https://www.oddsshark.com/api/scores/mlb?date=2026-08-16" || captured.BodyLimit() != 4*1024*1024 || captured.Timeout() != 10e9 {
		t.Fatalf("request=%s %v/%d", captured.URL(), captured.Timeout(), captured.BodyLimit())
	}
	if headers := captured.Headers(); len(headers) != 1 || headers[0].Name != "Referer" || headers[0].Value != "https://www.oddsshark.com/mlb/scores" {
		t.Fatalf("headers=%#v", headers)
	}
	if _, err := client.FetchGameLines("2026-02-30"); err != nil {
		t.Fatalf("frozen lexical date shape rejected: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count=%d", requestCount)
	}
	if _, err := client.FetchGameLines("20260816"); err == nil {
		t.Fatal("malformed date shape accepted")
	}
	if requestCount != 2 {
		t.Fatalf("invalid date reached transport: count=%d", requestCount)
	}
}

func TestOddsSharkAcceptsArrayAndAlternateKeys(t *testing.T) {
	payload := `[{"eventId":"7","gameDate":"2026-08-16T19:05:00Z","awayName":"Away","homeName":"Home","awayPrice":"120","homePrice":-130}]`
	client := NewOddsSharkClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
		return transport.Response{Status: 200, Body: []byte(payload)}, nil
	}}), ProductionOddsSharkEndpoints())
	lines, err := client.FetchGameLines("2026-08-16")
	if err != nil || len(lines) != 1 || lines[0].EventID != "7" {
		t.Fatalf("lines=%#v err=%v", lines, err)
	}
}

func TestOddsSharkRequiresARecognizedCollectionAndDropsIncompleteLines(t *testing.T) {
	for payload, wantError := range map[string]bool{`{}`: true, `{"scores":[{"away_team":"Away"}]}`: false} {
		client := NewOddsSharkClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
			return transport.Response{Status: 200, Body: []byte(payload)}, nil
		}}), ProductionOddsSharkEndpoints())
		lines, err := client.FetchGameLines("2026-08-16")
		if wantError && err == nil {
			t.Errorf("payload %s succeeded", payload)
		}
		if !wantError && (err != nil || len(lines) != 0) {
			t.Errorf("payload %s lines=%#v err=%v", payload, lines, err)
		}
	}
}
