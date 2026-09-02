package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStatcastReplacementPreservesScopesAndPrefersYahooIdentity(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "statcast.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `
INSERT INTO players(id,mlbam_id,name,mlbam_match_source,synced_at) VALUES(1,700001,'Seed','seed',1);
INSERT INTO players(id,yahoo_player_id,mlbam_id,name,mlbam_match_source,synced_at) VALUES(2,9,700001,'Yahoo','name+team+pos',1);
INSERT INTO players(id,mlbam_id,name,mlbam_match_source,synced_at) VALUES(3,700002,'Pitcher','seed',1);
INSERT INTO statcast_seasons(player_id,season,stat_group,pa,bbe,xwoba,fetched_at) VALUES(1,2025,'batting',10,5,.2,1);
INSERT INTO statcast_seasons(player_id,season,stat_group,pa,bbe,fastball_velo,fetched_at) VALUES(3,2026,'pitching',20,8,95,1)`); err != nil {
		t.Fatal(err)
	}
	xwoba, exitVelo := .401, 94.2
	written, err := database.ReplaceStatcastSnapshot(2026, "batting", []StatcastWrite{
		{MLBAMID: 700001, Season: 2026, StatGroup: "batting", PlateAppearances: 240, BattedBallEvents: 160, XWOBA: &xwoba, ExitVeloAverage: &exitVelo},
		{MLBAMID: 799999, Season: 2026, StatGroup: "batting", PlateAppearances: 12, BattedBallEvents: 8},
	})
	if err != nil || written != 1 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	var owner string
	if err := database.conn.QueryRowContext(testContext(), `SELECT p.name FROM statcast_seasons s JOIN players p ON p.id=s.player_id WHERE s.season=2026 AND s.stat_group='batting'`).Scan(&owner); err != nil || owner != "Yahoo" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	var unrelated int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM statcast_seasons WHERE (season=2025 AND stat_group='batting') OR (season=2026 AND stat_group='pitching')").Scan(&unrelated); err != nil || unrelated != 2 {
		t.Fatalf("unrelated=%d err=%v", unrelated, err)
	}
}

func TestStatcastReplacementWritesEachGroupToTheRoleMatchingTwoWayRow(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "twoway.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `
INSERT INTO players(id,mlbam_id,name,position_type,mlbam_match_source,synced_at) VALUES(1,660271,'Two Way','H','seed',1);
INSERT INTO players(id,mlbam_id,name,position_type,mlbam_match_source,synced_at) VALUES(2,660271,'Two Way','P','seed',1);
INSERT INTO players(id,yahoo_player_id,mlbam_id,name,position_type,mlbam_match_source,synced_at) VALUES(3,1000002,660271,'Two Way','P','seed',1);
INSERT INTO players(id,yahoo_player_id,mlbam_id,name,position_type,mlbam_match_source,synced_at) VALUES(4,1000001,660271,'Two Way (Batter)','B','name+twoway',1)`); err != nil {
		t.Fatal(err)
	}
	xwoba, velo := .403, 98.1
	if _, err := database.ReplaceStatcastSnapshot(2026, "batting", []StatcastWrite{{MLBAMID: 660271, Season: 2026, StatGroup: "batting", PlateAppearances: 586, XWOBA: &xwoba}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReplaceStatcastSnapshot(2026, "pitching", []StatcastWrite{{MLBAMID: 660271, Season: 2026, StatGroup: "pitching", PlateAppearances: 340, FastballVelo: &velo}}); err != nil {
		t.Fatal(err)
	}
	for group, want := range map[string]int64{"batting": 4, "pitching": 3} {
		var stored int64
		if err := database.conn.QueryRowContext(testContext(), "SELECT player_id FROM statcast_seasons WHERE season=2026 AND stat_group=?", group).Scan(&stored); err != nil || stored != want {
			t.Fatalf("%s row stored under player %d err=%v; want %d", group, stored, err, want)
		}
	}
	var read int64
	if err := database.conn.QueryRowContext(testContext(), `SELECT p2.id FROM players p2 WHERE p2.mlbam_id=660271 AND p2.position_type='P' ORDER BY CASE WHEN p2.mlbam_match_source='seed' THEN 0 ELSE 1 END DESC,p2.yahoo_player_id IS NULL,p2.id LIMIT 1`).Scan(&read); err != nil || read != 3 {
		t.Fatalf("pool pitching join resolves player %d err=%v; want 3", read, err)
	}
}

func TestStatcastValidationAndInjectedFailureRetainPriorSnapshot(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "rollback.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), "INSERT INTO players(id,mlbam_id,name,mlbam_match_source,synced_at) VALUES(1,7,'Prior','seed',1); INSERT INTO statcast_seasons(player_id,season,stat_group,pa,bbe,xwoba,fetched_at) VALUES(1,2026,'batting',100,50,.300,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReplaceStatcastSnapshot(2026, "batting", nil); err == nil {
		t.Fatal("empty snapshot replaced prior data")
	}
	if _, err := database.ReplaceStatcastSnapshot(2026, "pitching", []StatcastWrite{{MLBAMID: 7, Season: 2026, StatGroup: "batting"}}); err == nil {
		t.Fatal("mismatched row replaced prior data")
	}
	xwoba := .450
	_, err = database.replaceStatcastSnapshot(2026, "batting", []StatcastWrite{{MLBAMID: 7, Season: 2026, StatGroup: "batting", PlateAppearances: 200, BattedBallEvents: 100, XWOBA: &xwoba}}, func(int) error { return errors.New("stop") })
	if err == nil {
		t.Fatal("injected failure succeeded")
	}
	var pa int64
	var retained float64
	if err := database.conn.QueryRowContext(testContext(), "SELECT pa,xwoba FROM statcast_seasons WHERE season=2026 AND stat_group='batting'").Scan(&pa, &retained); err != nil || pa != 100 || retained != .300 {
		t.Fatalf("pa=%d xwoba=%f err=%v", pa, retained, err)
	}
}
