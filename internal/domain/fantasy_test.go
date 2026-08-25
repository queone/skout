package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFantasyEnumsNormalizationAndUnknownPreservation(t *testing.T) {
	for input, want := range map[string]ScoringType{
		"rotisserie": ScoringRotisserie, "roto": ScoringRotisserie,
		"head-to-head": ScoringHeadToHead, "head": ScoringHeadToHead,
		"points": ScoringPoints, "point": ScoringPoints, "custom": "custom",
	} {
		if got := ParseScoringType(input); got != want || got.String() != string(want) {
			t.Errorf("ParseScoringType(%q)=%q want=%q", input, got, want)
		}
	}
	for _, value := range []string{"C", "1B", "2B", "3B", "SS", "OF", "SP", "RP", "Util", "BN", "IL", "NA"} {
		if got := ParsePosition(value); got.String() != value {
			t.Errorf("ParsePosition(%q)=%q", value, got)
		}
	}
}

func TestRichFantasyRecordsRoundTripIndicatorsAndLogs(t *testing.T) {
	yahoo, mlbam := int64(7), int64(70)
	player := StoredFantasyPlayer{YahooPlayerID: &yahoo, MLBAMID: &mlbam, Name: "Ada Hitter", Role: "B", GameIndicator: GameIndicator{Kind: GameIndicatorBattingOrder, Order: 3}, Batting: [7]float64{100, .350, 20, 5, 18, 3, .280}}
	payload, err := json.Marshal(player)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StoredFantasyPlayer
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GameIndicator.Kind != GameIndicatorBattingOrder || decoded.GameIndicator.Order != 3 || decoded.MLBAMID == nil || *decoded.MLBAMID != 70 {
		t.Fatalf("decoded=%#v", decoded)
	}
	log := PlayerGameLog{Date: "2026-08-25", GameID: 9, Opponent: "@BOS", Line: "H 2 AB 4"}
	if !IsValidISODate(log.Date) || log.GameID <= 0 {
		t.Fatalf("log=%#v", log)
	}
}

func TestFantasyNameDateAndRosterHelpers(t *testing.T) {
	if got := CleanFantasyTeamName(" 💎 New York ⚾ "); got != "New York" {
		t.Fatalf("clean name=%q", got)
	}
	for value, want := range map[string]bool{"2026-02-28": true, "2024-02-29": true, "2026-02-29": false, "2026-2-01": false} {
		if got := IsValidISODate(value); got != want {
			t.Errorf("IsValidISODate(%q)=%v want=%v", value, got, want)
		}
	}
	roster := RosterWeekStats{Players: []PlayerWeekStats{{YahooPlayerID: 1, PositionType: "B"}, {YahooPlayerID: 2, PositionType: "P"}}}
	if len(roster.Batters()) != 1 || len(roster.Pitchers()) != 1 {
		t.Fatalf("role filtering failed: %#v", roster)
	}
	team := MatchupTeam{Wins: 6, CompletedGames: 3, LiveGames: 2, RemainingGames: 4}
	if team.Score() != 6 || team.TotalGames() != 9 {
		t.Fatalf("matchup helpers failed: %#v", team)
	}
}

func TestFantasyOrderingIsDeterministic(t *testing.T) {
	teams := []FantasyTeam{{TeamKey: "l.t.2"}, {TeamKey: "l.t.1"}}
	SortFantasyTeams(teams)
	if got := []string{teams[0].TeamKey, teams[1].TeamKey}; !reflect.DeepEqual(got, []string{"l.t.1", "l.t.2"}) {
		t.Fatalf("teams=%v", got)
	}
	players := []FantasyPlayer{{YahooPlayerID: 7}, {YahooPlayerID: 2}}
	SortFantasyPlayers(players)
	if players[0].YahooPlayerID != 2 {
		t.Fatalf("players=%#v", players)
	}
	slots := []FantasyRosterSlot{{TeamKey: "b", YahooPlayerID: 1}, {TeamKey: "a", YahooPlayerID: 2}, {TeamKey: "a", YahooPlayerID: 1}}
	SortFantasyRosterSlots(slots)
	if got := []int64{slots[0].YahooPlayerID, slots[1].YahooPlayerID, slots[2].YahooPlayerID}; !reflect.DeepEqual(got, []int64{1, 2, 1}) {
		t.Fatalf("slots=%#v", slots)
	}
}
