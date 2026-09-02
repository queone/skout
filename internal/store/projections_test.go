package store

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectionBlendUsesFrozenWeightsAndAvailableSourceNormalization(t *testing.T) {
	rows := []ProjectionWrite{
		{MLBAMID: 1, Season: 2026, Source: "steamer", StatGroup: "batting", PA: 100, HR: 10},
		{MLBAMID: 1, Season: 2026, Source: "zips", StatGroup: "batting", PA: 200, HR: 20},
		{MLBAMID: 1, Season: 2026, Source: "atc", StatGroup: "batting", PA: 300, HR: 30},
		{MLBAMID: 2, Season: 2026, Source: "steamer", StatGroup: "pitching", IP: 100, ERA: 3},
		{MLBAMID: 2, Season: 2026, Source: "atc", StatGroup: "pitching", IP: 200, ERA: 4},
	}
	output, err := BlendProjections(rows)
	if err != nil || len(output) != 7 {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	var hitter, pitcher ProjectionWrite
	for _, row := range output {
		if row.Source != "blend" {
			continue
		}
		if row.MLBAMID == 1 {
			hitter = row
		} else {
			pitcher = row
		}
	}
	if hitter.PA != 185 || hitter.HR != 18.5 {
		t.Fatalf("hitter blend=%#v", hitter)
	}
	if math.Abs(pitcher.IP-(100*.40+200*.25)/.65) > 1e-9 || math.Abs(pitcher.ERA-(3*.40+4*.25)/.65) > 1e-9 {
		t.Fatalf("pitcher blend=%#v", pitcher)
	}
	if _, err := BlendProjections(append(rows, rows[0])); err == nil {
		t.Fatal("duplicate source row accepted")
	}
}

func TestFanGraphsReplacementIsAtomicAndIsolatedBySeason(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "fangraphs.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `
INSERT INTO players(id,mlbam_id,name,mlb_team,eligible_positions,mlbam_match_source,synced_at) VALUES
(1,1,'Ace One','NYY','RP','seed',1),(2,2,'Ace Two','BOS','RP','seed',1);
INSERT INTO mlbam_season_stats(player_id,season,stat_group,sv,synced_at) VALUES
(1,2026,'pitching',5,1),(2,2026,'pitching',9,1);
INSERT INTO player_projections(player_id,season,source,stat_group,pa,fetched_at) VALUES(1,2025,'blend','batting',500,1);
INSERT INTO fangraphs_batted_ball(player_id,season,fb_pct,hr_fb_pct,fetched_at) VALUES(1,2025,.3,.1,1)`); err != nil {
		t.Fatal(err)
	}
	projections := []ProjectionWrite{
		{MLBAMID: 1, Season: 2026, Source: "steamer", StatGroup: "pitching", IP: 60},
		{MLBAMID: 2, Season: 2026, Source: "blend", StatGroup: "pitching", IP: 70},
	}
	batted := []FanGraphsBattedBallWrite{{MLBAMID: 1, Season: 2026, FlyBallPct: .4, HomeRunFB: .2}, {MLBAMID: 2, Season: 2026, FlyBallPct: .5, HomeRunFB: .3}}
	written, err := database.ReplaceFanGraphsSnapshot(2026, projections, batted, []CloserWrite{{Team: "NYY", Name: "Ace One"}})
	if err != nil || written != 2 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	var closers int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM players WHERE is_closer=1").Scan(&closers); err != nil || closers != 2 {
		t.Fatalf("closers=%d err=%v", closers, err)
	}
	var priorRows int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT (SELECT COUNT(*) FROM player_projections WHERE season=2025)+(SELECT COUNT(*) FROM fangraphs_batted_ball WHERE season=2025)").Scan(&priorRows); err != nil || priorRows != 2 {
		t.Fatalf("prior rows=%d err=%v", priorRows, err)
	}
	if _, err := database.ReplaceFanGraphsSnapshot(2026, nil, batted[:1], nil); err == nil {
		t.Fatal("incomplete replacement succeeded")
	}
	_, err = database.replaceFanGraphsSnapshot(2026, projections[:1], batted[:1], nil, func(stage string) error {
		if stage == "projections" {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil {
		t.Fatal("injected failure succeeded")
	}
	var currentRows int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM player_projections WHERE season=2026").Scan(&currentRows); err != nil || currentRows != 2 {
		t.Fatalf("current rows=%d err=%v", currentRows, err)
	}
	projection, err := database.BlendedProjection(2, 2026, "pitching")
	if err != nil || projection == nil || projection.IP != 70 {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
}

func TestECRReplacementUsesPrimaryFallbackAndRollbackRules(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "ecr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `
INSERT INTO players(id,yahoo_player_id,name,mlb_team,ecr,synced_at) VALUES
(1,10,'Primary','NYY',99,1),(2,11,'Fallback','BOS',98,1),(3,12,'Twin','SEA',97,1),(4,13,'Twin','SEA',96,1),(5,14,'Old','LAD',95,1);
INSERT INTO players(id,yahoo_player_id,mlbam_id,name,mlb_team,position_type,synced_at) VALUES
(6,15,660271,'Two Way','LAD','P',1),(7,16,660271,'Two Way (Batter)','LAD','B',1),(8,NULL,660271,'Two Way','LAD','P',1),(9,NULL,700009,'Fallback','BOS','B',1)`); err != nil {
		t.Fatal(err)
	}
	yahooID := int64(10)
	written, err := database.ReplaceECR([]ECRWrite{
		{YahooPlayerID: &yahooID, Name: "ignored", Team: "XXX", Rank: 1},
		{Name: "Fallback", Team: "bos", Rank: 2},
		{Name: "Twin", Team: "SEA", Rank: 3},
		{Name: "Two Way", Team: "LAD", Rank: 4},
	})
	if err != nil || written != 4 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	var ranked, primary, fallback int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM players WHERE ecr IS NOT NULL").Scan(&ranked); err != nil {
		t.Fatal(err)
	}
	_ = database.conn.QueryRowContext(testContext(), "SELECT ecr FROM players WHERE id=1").Scan(&primary)
	_ = database.conn.QueryRowContext(testContext(), "SELECT ecr FROM players WHERE id=2").Scan(&fallback)
	if ranked != 4 || primary != 1 || fallback != 2 {
		t.Fatalf("ranked=%d primary=%d fallback=%d", ranked, primary, fallback)
	}
	var twoWay string
	if err := database.conn.QueryRowContext(testContext(), "SELECT GROUP_CONCAT(id||'='||COALESCE(ecr,'-'),',') FROM (SELECT id,ecr FROM players WHERE id IN (6,7,8,9) ORDER BY id)").Scan(&twoWay); err != nil || twoWay != "6=4,7=4,8=-,9=-" {
		t.Fatalf("two-way and seed ranks=%q err=%v", twoWay, err)
	}
	_, err = database.replaceECR([]ECRWrite{{YahooPlayerID: &yahooID, Name: "Primary", Team: "NYY", Rank: 7}}, func() error { return errors.New("stop") })
	if err == nil {
		t.Fatal("injected failure succeeded")
	}
	_ = database.conn.QueryRowContext(testContext(), "SELECT ecr FROM players WHERE id=1").Scan(&primary)
	if primary != 1 {
		t.Fatalf("rollback rank=%d", primary)
	}
	if _, err := database.ReplaceECR([]ECRWrite{{Name: "Missing", Team: "XXX", Rank: 1}}); err == nil {
		t.Fatal("unresolvable snapshot succeeded")
	}
}
