package providers

import (
	"net/url"
	"strings"
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestSavantFixturesNormalizeBattingAndPitching(t *testing.T) {
	batting, err := ParseSavantCSV(fixtureResponse(t, "testdata/savant/batting.csv").Body, 2026, "batting")
	if err != nil || len(batting) != 1 {
		t.Fatalf("batting=%#v err=%v", batting, err)
	}
	if batting[0].MLBAMID != 700001 || batting[0].PlateAppearances != 240 || batting[0].XWOBA == nil || *batting[0].XWOBA != .401 || batting[0].FastballVelo != nil {
		t.Fatalf("batting row=%#v", batting[0])
	}
	pitching, err := ParseSavantCSV(fixtureResponse(t, "testdata/savant/pitching.csv").Body, 2026, "pitching")
	if err != nil || len(pitching) != 1 || pitching[0].FastballVelo == nil || *pitching[0].FastballVelo != 96.4 || pitching[0].GroundBallPercent == nil {
		t.Fatalf("pitching=%#v err=%v", pitching, err)
	}
}

func TestSavantDenominatorsAndShapeFailures(t *testing.T) {
	zeroBBE := []byte("player_id,pa,bbe,est_woba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n7,10,0,.3,90,5,40,20,8,27,.7\n")
	rows, err := ParseSavantCSV(zeroBBE, 2026, "batting")
	if err != nil || rows[0].ExitVeloAverage != nil || rows[0].BarrelPercent != nil || rows[0].HardHitPercent != nil || rows[0].XWOBA == nil {
		t.Fatalf("zero-BBE row=%#v err=%v", rows, err)
	}
	for _, payload := range [][]byte{
		[]byte("player_id,pa\n7,10\n"),
		[]byte("player_id,pa,bbe,est_woba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n0,10,2,.3,90,5,40,20,8,27,.7\n"),
		[]byte("player_id,pa,bbe,est_woba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n7,0,2,.3,90,5,40,20,8,27,.7\n"),
	} {
		if _, err := ParseSavantCSV(payload, 2026, "batting"); err == nil {
			t.Errorf("invalid CSV succeeded: %q", payload)
		}
	}
}

func TestSavantClientBuildsBoundedFrozenQueries(t *testing.T) {
	var captured []transport.ValidatedRequest
	client := NewSavantClient(transport.New(providerExecutor{execute: func(request transport.ValidatedRequest) (transport.Response, error) {
		captured = append(captured, request)
		if strings.Contains(request.URL(), "type=pitcher") {
			return fixtureResponse(t, "testdata/savant/pitching.csv"), nil
		}
		return fixtureResponse(t, "testdata/savant/batting.csv"), nil
	}}), ProductionSavantEndpoints())
	if _, err := client.FetchBatting(2026); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchPitching(2026); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("request count=%d", len(captured))
	}
	for _, request := range captured {
		target, _ := url.Parse(request.URL())
		if target.Query().Get("year") != "2026" || target.Query().Get("csv") != "true" || target.Query().Get("min") != "1" || request.Timeout() != savantTimeout || request.BodyLimit() != savantBodyLimit {
			t.Fatalf("request=%s timeout=%v limit=%d", request.URL(), request.Timeout(), request.BodyLimit())
		}
	}
	if _, err := client.FetchBatting(1999); err == nil {
		t.Fatal("unsupported season reached acquisition")
	}
}
