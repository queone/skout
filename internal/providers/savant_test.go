package providers

import (
	"net/url"
	"strings"
	"testing"

	"github.com/queone/skout/internal/transport"
)

func TestSavantFixturesNormalizeBattingAndPitching(t *testing.T) {
	battingPayload := fixtureResponse(t, "testdata/savant/batting.csv").Body
	batting, err := ParseSavantCSV(battingPayload, 2026, "batting")
	if err != nil || len(batting) != 1 {
		t.Fatalf("batting=%#v err=%v", batting, err)
	}
	if batting[0].MLBAMID != 700001 || batting[0].PlateAppearances != 240 || batting[0].XWOBA == nil || *batting[0].XWOBA != .401 || batting[0].FastballVelo != nil {
		t.Fatalf("batting row=%#v", batting[0])
	}
	pitchingPayload := fixtureResponse(t, "testdata/savant/pitching.csv").Body
	pitching, err := ParseSavantCSV(pitchingPayload, 2026, "pitching")
	if err != nil || len(pitching) != 1 || pitching[0].FastballVelo == nil || *pitching[0].FastballVelo != 96.4 || pitching[0].GroundBallPercent == nil {
		t.Fatalf("pitching=%#v err=%v", pitching, err)
	}
	for group, payload := range map[string][]byte{"batting": battingPayload, "pitching": pitchingPayload} {
		withBOM := append([]byte{0xef, 0xbb, 0xbf}, payload...)
		if rows, err := ParseSavantCSV(withBOM, 2026, group); err != nil || len(rows) != 1 {
			t.Fatalf("%s BOM rows=%#v err=%v", group, rows, err)
		}
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

func TestSavantCurrentShapesPreserveMetricsIndependentOfBlankBBE(t *testing.T) {
	batting := []byte("\xef\xbb\xbf\"last_name, first_name\",player_id,year,pa,bbe,xwoba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n\"Judge, Aaron\",592450,2026,100,,.401,95.1,20.0,61.0,20.0,10.0,28.0,1.050\n")
	rows, err := ParseSavantCSV(batting, 2026, "batting")
	if err != nil || len(rows) != 1 {
		t.Fatalf("batting rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.XWOBA == nil || row.StrikeoutPercent == nil || row.WalkPercent == nil || row.SprintSpeed == nil || row.OPS == nil {
		t.Fatalf("independent batting metrics were dropped: %#v", row)
	}
	if row.ExitVeloAverage != nil || row.BarrelPercent != nil || row.HardHitPercent != nil {
		t.Fatalf("BBE-dependent batting metrics survived blank BBE: %#v", row)
	}

	pitching := []byte("\xef\xbb\xbf\"last_name, first_name\",player_id,year,pa,bbe,ff_avg_speed,whiff_percent,chase_percent,groundballs_percent,k_percent,bb_percent\n\"Cole, Gerrit\",543037,2026,120,,96.4,31.0,29.0,44.0,28.0,7.0\n")
	rows, err = ParseSavantCSV(pitching, 2026, "pitching")
	if err != nil || len(rows) != 1 {
		t.Fatalf("pitching rows=%#v err=%v", rows, err)
	}
	row = rows[0]
	if row.FastballVelo == nil || row.WhiffPercent == nil || row.ChasePercent == nil || row.StrikeoutPercent == nil || row.WalkPercent == nil {
		t.Fatalf("independent pitching metrics were dropped: %#v", row)
	}
	if row.GroundBallPercent != nil {
		t.Fatalf("BBE-dependent pitching metric survived blank BBE: %#v", row)
	}
}

func TestSavantStripsOnlyOneLeadingBOMAndKeepsStrictCSVQuoting(t *testing.T) {
	header := "player_id,pa,bbe,est_woba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n"
	malformed := []byte(header + "7,10,2,.3,ba\"re,5,40,20,8,27,.7\n")
	if _, err := ParseSavantCSV(malformed, 2026, "batting"); err == nil {
		t.Fatal("malformed quote after byte zero was accepted")
	}
	doubleBOM := append([]byte{0xef, 0xbb, 0xbf, 0xef, 0xbb, 0xbf}, []byte(header+"7,10,2,.3,90,5,40,20,8,27,.7\n")...)
	if _, err := ParseSavantCSV(doubleBOM, 2026, "batting"); err == nil {
		t.Fatal("more than one leading BOM was accepted")
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
