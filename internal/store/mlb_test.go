package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMLBRosterReplacementPreservesUnrelatedRowsAndEnrichesIdentity(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "mlb.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `INSERT INTO players(id,mlbam_id,yahoo_player_id,name,mlb_team,display_position,position_type,eligible_positions,status,yahoo_rank,bat_side,synced_at) VALUES(1,10,20,'Existing','NYY','OF','H','OF','A',25,'R',100); INSERT INTO yahoo_teams(team_key,league_key,team_id,name,synced_at) VALUES('t.1','l.1',1,'Owners',100); INSERT INTO yahoo_roster_slots(team_key,player_id,slot_position,synced_at) VALUES('t.1',1,'OF',100); INSERT INTO mlb_team_active_rosters(team_abbr,mlbam_id,primary_type,status,fetched_at) VALUES('BOS',99,'H','A',100); INSERT INTO mlb_odds(game_pk,market,side,price,player_mlbam_id,sportsbook,fetched_at) VALUES(7,'moneyline','away',100,0,'Book',100)`); err != nil {
		t.Fatal(err)
	}
	rows := []RosterWrite{{MLBAMID: 10, Name: "Updated", Position: "OF", PrimaryType: "H", Status: "A", JerseyNumber: "7"}, {MLBAMID: 11, Name: "Pitcher", Position: "SP", PrimaryType: "P", Status: "A"}}
	if err := database.ReplaceMLBRoster("nyy", rows); err != nil {
		t.Fatal(err)
	}
	roster, err := database.MLBRoster("NYY")
	if err != nil || len(roster) != 2 {
		t.Fatalf("roster=%#v err=%v", roster, err)
	}
	if roster[0].Name != "Updated" || roster[0].Owner == nil || *roster[0].Owner != "Owners" || !roster[0].InYahooPool {
		t.Fatalf("enriched roster=%#v", roster[0])
	}
	var odds int64
	_ = database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM mlb_odds WHERE game_pk=7").Scan(&odds)
	if odds != 1 {
		t.Error("unrelated odds row changed")
	}
	var unrelatedRoster int64
	_ = database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM mlb_team_active_rosters WHERE team_abbr='BOS' AND mlbam_id=99").Scan(&unrelatedRoster)
	if unrelatedRoster != 1 {
		t.Error("unrelated team roster changed")
	}
	if err := database.ReplaceMLBRoster("NYY", nil); err == nil {
		t.Fatal("empty roster replaced prior nonempty roster")
	}
}

func TestHitterAverageAndWaiverCandidatesUseCompletedHistoryAndActiveRoles(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "fantasy-reads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.conn.ExecContext(testContext(), `INSERT INTO players(id,mlbam_id,name,display_position,position_type,eligible_positions,mlbam_match_source,synced_at) VALUES
(1,10,'Hitter','OF','H','OF','seed',1),(2,20,'Pitcher','SP','P','SP','seed',1);
INSERT INTO mlbam_season_stats(player_id,season,stat_group,g,pa,ab,h,hr,rbi,r,sb,bb,hbp,tb,ip,gs,synced_at) VALUES
(1,2021,'hitting',100,400,350,100,20,70,65,10,40,5,170,0,0,1),
(1,2022,'hitting',62,248,210,60,10,40,35,5,30,3,100,0,0,1),
(1,2026,'hitting',10,40,35,9,1,5,4,0,4,0,12,0,0,1),
(2,2026,'pitching',12,0,0,0,0,0,0,0,0,0,0,60,10,1);
INSERT INTO mlb_team_active_rosters(team_abbr,mlbam_id,primary_type,status,fetched_at) VALUES
('NYY',10,'H','A',1),('BOS',20,'P','A',1),('TB',30,'H','D10',1)`)
	if err != nil {
		t.Fatal(err)
	}
	average, err := database.HitterAverage(10, 2026)
	if err != nil || average == nil || average.PlateAppearances != 648 || average.HomeRuns != 30 || average.BattingAverage <= 0 {
		t.Fatalf("average=%#v err=%v", average, err)
	}
	missing, err := database.HitterAverage(999, 2026)
	if err != nil || missing != nil {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}
	candidates, err := database.WaiverCandidates()
	if err != nil || len(candidates) != 2 || candidates[0].Role != "H" || candidates[1].Role != "P" || candidates[1].GamesStarted != 10 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
}

func TestMLBRosterPrefersYahooThenSeedIdentity(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `
INSERT INTO players(id,mlbam_id,yahoo_player_id,name,position_type,mlbam_match_source,synced_at) VALUES
(1,10,20,'Yahoo','H','name',100),
(2,10,NULL,'Yahoo seed','H','seed',100),
(3,11,NULL,'Forty man','H','40man',100),
(4,11,NULL,'Seed','H','seed',100);
INSERT INTO mlb_team_active_rosters(team_abbr,mlbam_id,primary_type,status,fetched_at) VALUES
('NYY',10,'H','A',100),
('NYY',11,'H','A',100)`); err != nil {
		t.Fatal(err)
	}
	roster, err := database.MLBRoster("NYY")
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]string{}
	for _, player := range roster {
		names[player.MLBAMID] = player.Name
	}
	if names[10] != "Yahoo" || names[11] != "Seed" {
		t.Fatalf("selected identities=%#v", names)
	}
}

func TestMLBSeasonStatsReplaceOnlySuppliedScopesAndOwnershipIsOptional(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "stats.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prior := []SeasonStatWrite{{MLBAMID: 99, Name: "Prior", TeamAbbreviation: "BOS", StatGroup: "hitting", Games: 1, PlateAppearances: 4}}
	if err := database.ReplaceMLBSeasonStats(2025, prior); err != nil {
		t.Fatal(err)
	}
	writes := []SeasonStatWrite{{MLBAMID: 100, Name: "Hitter", TeamAbbreviation: "BOS", StatGroup: "hitting", Games: 2, PlateAppearances: 10, AtBats: 8, Hits: 3, Walks: 1, HitByPitch: 1, TotalBases: 5}, {MLBAMID: 101, Name: "Pitcher", TeamAbbreviation: "BOS", StatGroup: "pitching", Games: 2, InningsOuts: 20, EarnedRuns: 2, HitsAllowed: 5, PitcherWalks: 2, QualityStarts: 4}}
	if err := database.ReplaceMLBSeasonStats(2026, writes); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceMLBSeasonStats(2026, []SeasonStatWrite{{MLBAMID: 100, Name: "Hitter", TeamAbbreviation: "BOS", StatGroup: "hitting", Games: 3, PlateAppearances: 12}}); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceMLBSeasonStats(2026, []SeasonStatWrite{{MLBAMID: 101, Name: "Pitcher", TeamAbbreviation: "BOS", StatGroup: "pitching", Games: 3, InningsOuts: 23, QualityStarts: 0}}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM mlbam_season_stats WHERE season=2026").Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var priorCount, plateAppearances, qualityStarts int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM mlbam_season_stats WHERE season=2025 AND stat_group='hitting'").Scan(&priorCount); err != nil {
		t.Fatal(err)
	}
	if err := database.conn.QueryRowContext(testContext(), "SELECT pa FROM mlbam_season_stats WHERE season=2026 AND stat_group='hitting'").Scan(&plateAppearances); err != nil {
		t.Fatal(err)
	}
	if err := database.conn.QueryRowContext(testContext(), "SELECT qs FROM mlbam_season_stats WHERE season=2026 AND stat_group='pitching'").Scan(&qualityStarts); err != nil {
		t.Fatal(err)
	}
	if priorCount != 1 || plateAppearances != 12 || qualityStarts != 4 {
		t.Fatalf("prior=%d plate appearances=%d quality starts=%d", priorCount, plateAppearances, qualityStarts)
	}
	counts, err := database.MLBLocalPlayerCounts()
	if err != nil || len(counts) != 0 {
		t.Fatalf("optional counts=%#v err=%v", counts, err)
	}
	ownership, err := database.MLBLocalPitcherOwnership("")
	if err != nil || ownership["pitcher"] != [3]bool{false, false, false} {
		t.Fatalf("ownership=%#v err=%v", ownership, err)
	}
}

func TestOwnershipFreshnessReadsSyncLog(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "ownership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if value, err := database.OwnershipSyncedAt(); err != nil || value != nil {
		t.Fatalf("empty value=%v err=%v", value, err)
	}
	if _, err := database.conn.ExecContext(testContext(), "INSERT INTO sync_log(table_name,synced_at) VALUES('rosters',123)"); err != nil {
		t.Fatal(err)
	}
	value, err := database.OwnershipSyncedAt()
	if err != nil || value == nil || *value != 123 {
		t.Fatalf("value=%v err=%v", value, err)
	}
}
