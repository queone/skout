package providers

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/queone/skout/internal/domain"
)

func yahooFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/yahoo/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestYahooFantasyFixturesNormalizeCompleteWorkflow(t *testing.T) {
	settings, err := ParseLeagueSettings("mlb.l.1", yahooFixture(t, "league-settings.json"))
	if err != nil || settings.CurrentWeek == nil || *settings.CurrentWeek != 7 || len(settings.Categories) != 2 || len(settings.RosterPositions) != 2 || settings.League.ScoringType != domain.ScoringHeadToHead {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
	teams, err := ParseStandings("mlb.l.1", yahooFixture(t, "standings.json"))
	if err != nil || len(teams) != 2 || teams[0].Wins != 107 || teams[0].Moves != 29 || teams[1].Name != "Opponents" {
		t.Fatalf("teams=%#v err=%v", teams, err)
	}
	rosters, err := ParseTeamRosters([]string{"mlb.l.1.t.1", "mlb.l.1.t.2"}, [][]byte{yahooFixture(t, "roster-team-1.json"), yahooFixture(t, "roster-team-2.json")})
	if err != nil || len(rosters.Players) != 2 || len(rosters.Slots) != 2 {
		t.Fatalf("rosters=%#v err=%v", rosters, err)
	}
	if rosters.Slots[0].SlotPosition != domain.PositionBench || rosters.Players[0].InjuryStatus != "DTD" || !reflect.DeepEqual(rosters.Players[0].EligiblePositions, []domain.Position{"1B", "OF", "Util"}) {
		t.Fatalf("selected or eligible positions=%#v", rosters)
	}
	freeAgents, err := ParseFreeAgents(yahooFixture(t, "free-agents.json"))
	if err != nil || len(freeAgents) != 2 || freeAgents[0].YahooRank == nil || *freeAgents[0].YahooRank != 31 || freeAgents[0].PercentOwned == nil || *freeAgents[0].PercentOwned != 12 {
		t.Fatalf("free agents=%#v err=%v", freeAgents, err)
	}
	matchups, err := ParseScoreboard(yahooFixture(t, "matchup.json"))
	if err != nil || len(matchups) != 1 || matchups[0].Week != 7 || matchups[0].Teams[0].Stats["7"] != "12" || matchups[0].Teams[0].TotalGames() != 10 {
		t.Fatalf("matchups=%#v err=%v", matchups, err)
	}
	weekly, err := ParseRosterWeekStats("mlb.l.1.t.1", 7, yahooFixture(t, "weekly-stats.json"))
	if err != nil || len(weekly.Players) != 1 || weekly.Players[0].HomeRuns != 1 || weekly.Players[0].HAB != "3/10" {
		t.Fatalf("weekly=%#v err=%v", weekly, err)
	}
	// Daily and weekly payloads deliberately share the same normalizer; only
	// the public client decides which endpoint is dormant during this slice.
	daily, err := ParseRosterWeekStats("mlb.l.1.t.1", 7, yahooFixture(t, "weekly-stats.json"))
	if err != nil || !reflect.DeepEqual(daily, weekly) {
		t.Fatalf("daily=%#v weekly=%#v err=%v", daily, weekly, err)
	}
}

func TestYahooFantasyNumericArraySingletonAndMissingEchoShapes(t *testing.T) {
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(yahooFixture(t, "shape-variants.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	teams, err := ParseStandings("mlb.l.1", fixture["numeric"])
	if err != nil || len(teams) != 1 || teams[0].TeamID != 1 || teams[0].Wins != 4 {
		t.Fatalf("numeric teams=%#v err=%v", teams, err)
	}
	for _, name := range []string{"array", "singleton"} {
		players, err := ParseFreeAgents(fixture[name])
		if err != nil || len(players) != 1 || len(players[0].EligiblePositions) != 1 {
			t.Errorf("%s players=%#v err=%v", name, players, err)
		}
	}
	rosters, err := ParseTeamRosters([]string{"mlb.l.1.t.1"}, [][]byte{fixture["missing_echo"]})
	if err != nil || len(rosters.Slots) != 1 || rosters.Slots[0].TeamKey != "mlb.l.1.t.1" {
		t.Fatalf("missing echo roster=%#v err=%v", rosters, err)
	}
}

func TestYahooFantasyRealFieldArraysAvoidCollidingNestedPositionsAndKeeperStatus(t *testing.T) {
	payload := []byte(`{"data":{"team":[[{"team_key":"mlb.l.1.t.1"},{"name":"Testers"}],{"roster":{"0":{"players":{"0":{"player":[[{"player_id":501},{"name":{"full":"Ada Bencher"}},{"is_keeper":{"status":false}},{"display_position":"1B,OF"},{"eligible_positions":[{"position":"1B"},{"position":"OF"}]}],{"selected_position":[{"coverage_type":"date"},{"position":"BN"}]}]}}}}}]}}`)
	rosters, err := ParseTeamRosters([]string{"mlb.l.1.t.1"}, [][]byte{payload})
	if err != nil || len(rosters.Players) != 1 || rosters.Players[0].Name != "Ada Bencher" || rosters.Players[0].InjuryStatus != "" || rosters.Slots[0].SlotPosition != domain.PositionBench {
		t.Fatalf("rosters=%#v err=%v", rosters, err)
	}
}

func TestYahooFantasyRejectsIncompleteCollectionsAndMalformedPayloads(t *testing.T) {
	empty := []byte(`{"data":[]}`)
	checks := []func() error{
		func() error { _, err := ParseLeagueSettings("mlb.l.1", empty); return err },
		func() error { _, err := ParseStandings("mlb.l.1", empty); return err },
		func() error { _, err := ParseLeagueRosters("mlb.l.1", empty); return err },
		func() error { _, err := ParseRosterWeekStats("mlb.l.1.t.1", 7, empty); return err },
		func() error { _, err := ParseStandings("mlb.l.1", []byte(`{`)); return err },
	}
	for index, check := range checks {
		if err := check(); err == nil {
			t.Errorf("check %d succeeded", index)
		} else {
			var typed *YahooFantasyError
			if !errors.As(err, &typed) || !strings.Contains(err.Error(), "Yahoo fantasy") {
				t.Errorf("check %d error=%v", index, err)
			}
		}
	}
	matchups, err := ParseScoreboard(empty)
	if err != nil || len(matchups) != 0 {
		t.Fatalf("empty scoreboard=%#v err=%v", matchups, err)
	}
}

func TestYahooFantasyPaginationAndRankSelectionAreBounded(t *testing.T) {
	starts, err := BoundedPageStarts(51, 25)
	if err != nil || !reflect.DeepEqual(starts, []int{0, 25, 50}) {
		t.Fatalf("starts=%v err=%v", starts, err)
	}
	if _, err := BoundedPageStarts(1, 0); err == nil {
		t.Fatal("zero page size succeeded")
	}
	if _, err := BoundedPageStarts(501, 25); err == nil {
		t.Fatal("unbounded pages succeeded")
	}
	payload := []byte(`{"data":[{"team_key":"mlb.l.1.t.1","players":[{"player_id":101,"full":"Previous Actual","position":"OF","player_ranks":[{"player_rank":{"rank_type":"OR","rank_value":"22"}},{"player_rank":{"rank_season":"2026","rank_type":"S","rank_value":"22"}},{"player_rank":{"rank_season":"2025","rank_type":"S","rank_value":"321"}}]},{"player_id":202,"full":"Current Actual","position":"SP","player_ranks":[{"player_rank":{"rank_type":"OR","rank_value":"12"}},{"player_rank":{"rank_season":"2026","rank_type":"S","rank_value":"44"}},{"player_rank":{"rank_season":"2025","rank_type":"S","rank_value":"90"}}]}]}]}`)
	roster, err := ParseLeagueRosters("mlb.l.1", payload)
	if err != nil || roster.Players[0].YahooRank == nil || *roster.Players[0].YahooRank != 321 || roster.Players[1].YahooRank == nil || *roster.Players[1].YahooRank != 44 {
		t.Fatalf("roster=%#v err=%v", roster, err)
	}
}
