package domain

import (
	"encoding/json"
	"testing"
)

func TestMLBRecordsUseFrozenJSONNamesAndStableTeamOrdering(t *testing.T) {
	team := Team{ID: 2, Name: "Beta", Location: "B", ClubName: "Club", Abbreviation: "BET", LeagueID: 103}
	data, err := json.Marshal(team)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"id":2,"name":"Beta","location":"B","club_name":"Club","abbreviation":"BET","league_id":103}`; got != want {
		t.Fatalf("JSON=%s want=%s", got, want)
	}
	teams := []Team{team, {ID: 3, Name: "Alpha"}, {ID: 1, Name: "Beta"}}
	SortTeams(teams)
	if teams[0].Name != "Alpha" || teams[1].ID != 1 || teams[2].ID != 2 {
		t.Fatalf("sorted=%#v", teams)
	}
}
