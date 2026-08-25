package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/queone/skout/internal/domain"
)

func fantasySnapshot(league string) FantasySnapshotWrite {
	week := 7
	one, two := 99.0, 75.0
	rankOne, rankTwo, rankThree := int64(1), int64(2), int64(3)
	return FantasySnapshotWrite{
		League:      domain.League{LeagueKey: league, Name: "League", Season: 2026, NumTeams: 2, ScoringType: domain.ScoringHeadToHead},
		CurrentWeek: &week,
		Categories:  []CategoryWrite{{StatID: 7, Abbreviation: "R", Name: "Runs", SortOrder: 1, Sequence: 1}},
		Positions:   []PositionWrite{{Position: "OF", Count: 1}},
		Teams: []domain.FantasyTeam{
			{TeamKey: league + ".t.1", LeagueKey: league, TeamID: 1, Name: "One", ManagerName: "A", WaiverPriority: 1, FAABBalance: 65, Rank: 1},
			{TeamKey: league + ".t.2", LeagueKey: league, TeamID: 2, Name: "💎 Two", ManagerName: "B", WaiverPriority: 2, FAABBalance: 50, Rank: 2},
		},
		Players: []domain.FantasyPlayer{
			{YahooPlayerID: 101, Name: "Ada Hitter", MLBTeam: "NYY", DisplayPosition: "OF", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}, PercentOwned: &one, YahooRank: &rankOne},
			{YahooPlayerID: 102, Name: "Grace Hitter", MLBTeam: "BOS", DisplayPosition: "OF", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}, PercentOwned: &two, YahooRank: &rankTwo},
			{YahooPlayerID: 103, Name: "Free Agent", MLBTeam: "TB", DisplayPosition: "OF", PositionType: "B", EligiblePositions: []domain.Position{domain.PositionOutfield}, YahooRank: &rankThree},
		},
		Slots: []domain.FantasyRosterSlot{
			{TeamKey: league + ".t.1", YahooPlayerID: 101, SlotPosition: domain.PositionOutfield},
			{TeamKey: league + ".t.2", YahooPlayerID: 102, SlotPosition: domain.PositionOutfield},
		},
	}
}

func TestFantasySnapshotIsAtomicScopedAndPreservesStablePlayerIDs(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "fantasy.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first := fantasySnapshot("mlb.l.1")
	if err := database.ReplaceFantasySnapshot(first); err != nil {
		t.Fatal(err)
	}
	var playerID int64
	if err := database.conn.QueryRowContext(context.Background(), "SELECT id FROM players WHERE yahoo_player_id=101").Scan(&playerID); err != nil {
		t.Fatal(err)
	}
	if err := database.ReplaceFantasySnapshot(fantasySnapshot("mlb.l.2")); err != nil {
		t.Fatal(err)
	}
	first.Players[0].Name = "Ada Updated"
	if err := database.ReplaceFantasySnapshot(first); err != nil {
		t.Fatal(err)
	}
	var replacedID int64
	if err := database.conn.QueryRowContext(context.Background(), "SELECT id FROM players WHERE yahoo_player_id=101").Scan(&replacedID); err != nil {
		t.Fatal(err)
	}
	teams, err := database.FantasyTeams("mlb.l.1")
	if err != nil || len(teams) != 2 || teams[1].Name != "Two" || replacedID != playerID {
		t.Fatalf("teams=%#v player ids=%d/%d err=%v", teams, playerID, replacedID, err)
	}
	players, err := database.FantasyPlayers("mlb.l.1")
	if err != nil || len(players) != 3 || players[0].Name != "Ada Updated" || players[2].Owner != nil {
		t.Fatalf("players=%#v err=%v", players, err)
	}
	other, err := database.FantasyTeams("mlb.l.2")
	if err != nil || len(other) != 2 {
		t.Fatalf("other=%#v err=%v", other, err)
	}
	week, err := database.FantasyCurrentWeek("mlb.l.1")
	season, seasonErr := database.FantasySeason("mlb.l.1")
	if err != nil || seasonErr != nil || week == nil || *week != 7 || season == nil || *season != 2026 {
		t.Fatalf("week=%v season=%v errors=%v/%v", week, season, err, seasonErr)
	}
}

func TestFantasyPlayersJoinsRichRoleDistinctEnrichmentAndActiveInjury(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "rich.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ReplaceFantasySnapshot(fantasySnapshot("mlb.l.1")); err != nil {
		t.Fatal(err)
	}
	_, err = database.conn.ExecContext(testContext(), `UPDATE players SET mlbam_id=10,bat_side='R',birth_date='1995-01-02',is_closer=1,ecr=8 WHERE yahoo_player_id=101;
INSERT INTO players(id,mlbam_id,name,position_type,mlbam_match_source,synced_at) VALUES(100,10,'Ada Seed','H','seed',1);
INSERT INTO mlbam_season_stats(player_id,season,stat_group,g,pa,so_bat,bb,obp,r,hr,rbi,sb,avg,synced_at) VALUES((SELECT id FROM players WHERE yahoo_player_id=101),2026,'hitting',50,200,30,20,.350,30,8,25,4,.275,1);
INSERT INTO mlbam_season_stats(player_id,season,stat_group,g,pa,so_bat,bb,synced_at) VALUES((SELECT id FROM players WHERE yahoo_player_id=101),2025,'hitting',150,600,100,60,1);
INSERT INTO statcast_seasons(player_id,season,stat_group,pa,bbe,xwoba,exit_velo_avg,barrel_pct,hard_hit_pct,strikeout_pct,walk_pct,sprint_speed,ops,fetched_at) VALUES((SELECT id FROM players WHERE yahoo_player_id=101),2026,'batting',200,150,.360,91.2,12.0,46.0,18.0,10.0,28.1,.850,1);
INSERT INTO fangraphs_batted_ball(player_id,season,fb_pct,hr_fb_pct,fetched_at) VALUES((SELECT id FROM players WHERE yahoo_player_id=101),2026,40,20,1);
INSERT INTO players(id,mlbam_id,yahoo_player_id,name,mlb_team,display_position,position_type,eligible_positions,pitch_hand,yahoo_rank,synced_at) VALUES(101,10,104,'Ada Pitcher','NYY','SP','P','SP','L',4,1);
INSERT INTO yahoo_free_agents(league_key,player_id,synced_at) VALUES('mlb.l.1',101,1);
INSERT INTO mlbam_season_stats(player_id,season,stat_group,g,gs,ip,k,era,whip,synced_at) VALUES(101,2026,'pitching',10,10,60,70,3.2,1.1,1);
INSERT INTO statcast_seasons(player_id,season,stat_group,pa,bbe,fastball_velo,whiff_pct,chase_pct,gb_pct,strikeout_pct,walk_pct,fetched_at) VALUES(101,2026,'pitching',250,100,97.1,30,28,42,25,7,1);
INSERT INTO mlb_team_active_rosters(team_abbr,mlbam_id,primary_type,status,fetched_at) VALUES('NYY',10,'H','D10',1)`)
	if err != nil {
		t.Fatal(err)
	}
	players, err := database.FantasyPlayers("mlb.l.1")
	if err != nil {
		t.Fatal(err)
	}
	row := players[0]
	if row.MLBAMID == nil || *row.MLBAMID != 10 || row.Role != "B" || row.Status != "IL10" || row.Batting[0] != 200 || row.HittingAdvanced[0] == nil || *row.HittingAdvanced[0] != .360 || !row.IsCloser || row.Owner == nil || *row.Owner != "One" {
		t.Fatalf("row=%#v", row)
	}
	var pitcher *StoredFantasyPlayer
	for index := range players {
		if players[index].YahooPlayerID != nil && *players[index].YahooPlayerID == 104 {
			pitcher = &players[index]
		}
	}
	if pitcher == nil || pitcher.Role != "P" || pitcher.Hand != "L" || pitcher.Pitching[0] != 60 || pitcher.PitchingAdvanced[0] == nil || *pitcher.PitchingAdvanced[0] != 97.1 || pitcher.HittingAdvanced[0] != nil || pitcher.Status != "" {
		t.Fatalf("role-distinct pitcher=%#v", pitcher)
	}
}

func TestFantasySnapshotRejectsIncompleteInputAndRollsBackInjectedFailure(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prior := fantasySnapshot("mlb.l.1")
	if err := database.ReplaceFantasySnapshot(prior); err != nil {
		t.Fatal(err)
	}
	invalid := prior
	invalid.Players = append(invalid.Players, invalid.Players[0])
	if err := database.ReplaceFantasySnapshot(invalid); err == nil {
		t.Fatal("duplicate player identity accepted")
	}
	if _, err := database.conn.ExecContext(context.Background(), "CREATE TRIGGER fail_fantasy_update BEFORE UPDATE OF name ON players WHEN NEW.name='Broken' BEGIN SELECT RAISE(FAIL,'injected'); END"); err != nil {
		t.Fatal(err)
	}
	broken := prior
	broken.Players = append([]domain.FantasyPlayer(nil), prior.Players...)
	broken.Players[0].Name = "Broken"
	if err := database.ReplaceFantasySnapshot(broken); err == nil {
		t.Fatal("injected write failure succeeded")
	}
	players, err := database.FantasyPlayers("mlb.l.1")
	if err != nil || len(players) != 3 || players[0].Name != "Ada Hitter" {
		t.Fatalf("prior snapshot changed: %#v err=%v", players, err)
	}
}

func TestFantasySyncSnapshotRollsBackTablesAndWeeklyPayloadsTogether(t *testing.T) {
	database, err := OpenAt(filepath.Join(t.TempDir(), "weekly-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prior := fantasySnapshot("mlb.l.1")
	priorCommand := FantasyCommandSnapshotWrite{Dataset: "match_scoreboard", Source: "yahoo", Scope: "mlb.l.1:7", Version: "v1", Payload: `{"old":true}`}
	if err := database.ReplaceFantasySyncSnapshot(prior, []FantasyCommandSnapshotWrite{priorCommand}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `CREATE TRIGGER fail_weekly_snapshot BEFORE UPDATE OF payload ON command_snapshots WHEN NEW.payload='{"new":true}' BEGIN SELECT RAISE(FAIL,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	changed := prior
	changed.Players = append([]domain.FantasyPlayer(nil), prior.Players...)
	changed.Players[0].Name = "Changed"
	command := priorCommand
	command.Payload = `{"new":true}`
	if err := database.ReplaceFantasySyncSnapshot(changed, []FantasyCommandSnapshotWrite{command}); err == nil {
		t.Fatal("injected weekly snapshot failure succeeded")
	}
	players, err := database.FantasyPlayers("mlb.l.1")
	if err != nil || players[0].Name != "Ada Hitter" {
		t.Fatalf("fantasy tables changed: %#v err=%v", players, err)
	}
	snapshot, err := database.CommandSnapshot("match_scoreboard", "yahoo", "mlb.l.1:7")
	if err != nil || snapshot == nil || snapshot.Payload != `{"old":true}` {
		t.Fatalf("weekly snapshot=%#v err=%v", snapshot, err)
	}
}

func TestFantasyIdentityReconciliationIsExactUniqueRoleDistinctAndTimestamped(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "identities.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ReplaceFantasySnapshot(fantasySnapshot("mlb.l.1")); err != nil {
		t.Fatal(err)
	}
	candidates := []IdentityCandidate{
		{MLBAMID: 700001, Name: "Ada Hitter", Team: "NYY", Role: "B"},
		{MLBAMID: 700001, Name: "Ada Hitter", Team: "NYY", Role: "B"},
		{MLBAMID: 700002, Name: "Grace Hitter", Team: "BOS", Role: "B"},
		{MLBAMID: 700003, Name: "Grace Hitter", Team: "BOS", Role: "B"},
		{MLBAMID: 700001, Name: "Ada Hitter", Team: "NYY", Role: "P"},
	}
	updated, err := database.ReconcileMLBIdentities(candidates)
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	var mlbam, matchedAt int64
	var source string
	if err := database.conn.QueryRowContext(context.Background(), "SELECT mlbam_id,mlbam_match_source,mlbam_matched_at FROM players WHERE yahoo_player_id=101").Scan(&mlbam, &source, &matchedAt); err != nil {
		t.Fatal(err)
	}
	if mlbam != 700001 || source != "name+team+pos" || matchedAt != 2_000_000_000 {
		t.Fatalf("identity=%d/%s/%d", mlbam, source, matchedAt)
	}
	var unresolved int64
	if err := database.conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM players WHERE yahoo_player_id IN (102,103) AND mlbam_id IS NULL").Scan(&unresolved); err != nil || unresolved != 2 {
		t.Fatalf("unresolved=%d err=%v", unresolved, err)
	}
}
