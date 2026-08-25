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
