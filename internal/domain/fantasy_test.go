package domain

import (
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
