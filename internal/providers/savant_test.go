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
	zeroBBEPitching := []byte("player_id,pa,bbe,ff_avg_speed,whiff_percent,chase_percent,groundballs_percent,k_percent,bb_percent\n8,12,0,96.4,31.0,29.0,44.0,28.0,7.0\n")
	rows, err = ParseSavantCSV(zeroBBEPitching, 2026, "pitching")
	if err != nil || len(rows) != 1 || rows[0].GroundBallPercent != nil || rows[0].FastballVelo == nil || rows[0].WhiffPercent == nil || rows[0].ChasePercent == nil {
		t.Fatalf("zero-BBE pitching row=%#v err=%v", rows, err)
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

func TestSavantBlankBattedBallCountKeepsRatesWhenPlateAppearancesExist(t *testing.T) {
	batting := []byte("\xef\xbb\xbf\"last_name, first_name\",player_id,year,pa,bbe,xwoba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n\"Judge, Aaron\",592450,2026,100,,.401,95.1,20.0,61.0,20.0,10.0,28.0,1.050\n")
	rows, err := ParseSavantCSV(batting, 2026, "batting")
	if err != nil || len(rows) != 1 {
		t.Fatalf("batting rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.XWOBA == nil || row.StrikeoutPercent == nil || row.WalkPercent == nil || row.SprintSpeed == nil || row.OPS == nil {
		t.Fatalf("independent batting metrics were dropped: %#v", row)
	}
	if row.BattedBallEvents != 0 || row.ExitVeloAverage == nil || *row.ExitVeloAverage != 95.1 || row.BarrelPercent == nil || *row.BarrelPercent != 20.0 || row.HardHitPercent == nil || *row.HardHitPercent != 61.0 {
		t.Fatalf("blank batted-ball count dropped batting rates backed by plate appearances: %#v", row)
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
	if row.GroundBallPercent == nil || *row.GroundBallPercent != 44.0 {
		t.Fatalf("blank batted-ball count dropped the ground-ball rate backed by batters faced: %#v", row)
	}

	noDenominator := []byte("player_id,pa,bbe,est_woba,exit_velocity_avg,brl_percent,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg\n7,0,,,90,5,40,20,8,27,.7\n")
	rows, err = ParseSavantCSV(noDenominator, 2026, "batting")
	if err != nil || len(rows) != 1 || rows[0].ExitVeloAverage != nil || rows[0].BarrelPercent != nil || rows[0].HardHitPercent != nil {
		t.Fatalf("blank batted-ball count without plate appearances kept batting rates: rows=%#v err=%v", rows, err)
	}
}

func TestSavantFastballVeloFallsBackToSinkerThenCutter(t *testing.T) {
	header := "player_id,pa,bbe,ff_avg_speed,si_avg_speed,fc_avg_speed,whiff_percent,chase_percent,groundballs_percent,k_percent,bb_percent\n"
	payload := []byte(header + "1,726,,,95.1,,32.8,36.8,58.5,27.8,5.4\n2,237,,,98.3,98.8,27.2,28.7,53,23.6,12.2\n3,100,,,,90.5,20,25,40,20,8\n4,50,,,,,20,25,40,20,8\n5,300,,97.2,94.0,,30,30,45,28,7\n")
	rows, err := ParseSavantCSV(payload, 2026, "pitching")
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	want := []*float64{ptrFloat(95.1), ptrFloat(98.3), ptrFloat(90.5), nil, ptrFloat(97.2)}
	for index, row := range rows {
		if (row.FastballVelo == nil) != (want[index] == nil) || row.FastballVelo != nil && *row.FastballVelo != *want[index] {
			t.Fatalf("row %d fastball velo=%v want %v", index+1, row.FastballVelo, want[index])
		}
	}
	legacy := []byte("player_id,pa,bbe,ff_avg_speed,whiff_percent,chase_percent,groundballs_percent,k_percent,bb_percent\n6,120,,,31.0,29.0,44.0,28.0,7.0\n")
	if rows, err := ParseSavantCSV(legacy, 2026, "pitching"); err != nil || len(rows) != 1 || rows[0].FastballVelo != nil {
		t.Fatalf("legacy shape without sinker columns: rows=%#v err=%v", rows, err)
	}
}

func ptrFloat(value float64) *float64 { return &value }

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
		if target.Query().Get("type") == "pitcher" && !strings.Contains(target.Query().Get("selections"), "ff_avg_speed,si_avg_speed,fc_avg_speed,") {
			t.Fatalf("pitching request lacks sinker and cutter speeds: %s", request.URL())
		}
	}
	if _, err := client.FetchBatting(1999); err == nil {
		t.Fatal("unsupported season reached acquisition")
	}
}
