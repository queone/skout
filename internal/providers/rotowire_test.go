package providers

import (
	"strings"
	"testing"
	"time"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/transport"
)

type providerTestClock struct{ now time.Time }

func (clock providerTestClock) Now() time.Time { return clock.now }

func TestRotoWireFixtureParsesConfirmedAndExpectedLineups(t *testing.T) {
	rows := ParseRotoWireDailyLineups(string(fixtureResponse(t, "testdata/rotowire/daily-lineups.html").Body))
	if len(rows) != 2 {
		t.Fatalf("rows=%#v", rows)
	}
	if rows[0].AwayTeam != "NYY" || rows[0].HomeTeam != "BOS" || !rows[0].Confirmed || rows[0].AwayPitcher != "Gerrit Cole" || len(rows[0].AwayPlayers) != 2 || rows[0].AwayPlayers[1] != "Aaron Judge" {
		t.Fatalf("confirmed row=%#v", rows[0])
	}
	if rows[1].AwayTeam != "AZ" || rows[1].HomeTeam != "ATH" || rows[1].Confirmed {
		t.Fatalf("expected row=%#v", rows[1])
	}
}

func TestRotoWireFetchCachesForTwoMinutes(t *testing.T) {
	calls := 0
	client := NewRotoWireClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		calls++
		if request.Timeout() != rotoWireTimeout || request.BodyLimit() != rotoWireBodyLimit {
			t.Fatalf("bounds=%v/%d", request.Timeout(), request.BodyLimit())
		}
		return fixtureResponse(t, "testdata/rotowire/daily-lineups.html"), nil
	}}), ProductionRotoWireEndpoints())
	disk := cache.WithClock(t.TempDir(), providerTestClock{time.Unix(2_000_000_000, 0)})
	first, err := client.FetchCached(disk)
	if err != nil || len(first) != 2 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := client.FetchCached(disk)
	if err != nil || len(second) != 2 || calls != 1 {
		t.Fatalf("second=%#v calls=%d err=%v", second, calls, err)
	}
}

func TestRotoWireBoundsFailures(t *testing.T) {
	for _, response := range []transport.Response{{Status: 503}, {Status: 200, Body: []byte("<html>no games</html>")}} {
		client := NewRotoWireClient(transport.New(providerExecutor{execute: func(transport.ValidatedRequest) (transport.Response, error) {
			return response, nil
		}}), ProductionRotoWireEndpoints())
		_, err := client.FetchCached(cache.WithClock(t.TempDir(), providerTestClock{time.Unix(2_000_000_000, 0)}))
		if err == nil || !strings.Contains(err.Error(), "RotoWire") {
			t.Errorf("response=%#v error=%v", response, err)
		}
	}
}
