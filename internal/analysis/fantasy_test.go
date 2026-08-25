package analysis

import (
	"math"
	"testing"

	"github.com/queone/skout/internal/domain"
)

func TestPitcherRoleUsesProbableRecentSeasonAndEligibilityEvidence(t *testing.T) {
	if ClassifyPitcher("RP", true, nil, 0, 0) != Starter || ClassifyPitcher("RP", false, []RecentAppearance{{1}, {1}, {0}, {1}}, 0, 0) != Starter || ClassifyPitcher("RP", false, nil, 10, 7) != Starter || ClassifyPitcher("SP", false, nil, 10, 2) != Reliever || ClassifyPitcherPositions("RP,SP") != Starter {
		t.Fatal("pitcher role priority differs from frozen behavior")
	}
}

func TestProjectionWindowsScaleBlendAndFallback(t *testing.T) {
	hitter := NextHitterWindow(&HitterWindow{PA: 100, Runs: 20, Average: .3}, &HitterWindow{PA: 20, Runs: 8, Average: .4}, 20)
	if math.Abs(hitter.Runs-5.2) > .0001 || math.Abs(hitter.Average-.33) > .0001 {
		t.Fatalf("hitter=%#v", hitter)
	}
	pitcher := NextPitcherWindow(&PitcherWindow{IP: 100, Wins: 10, QualityStarts: 12, ERA: 3}, &PitcherWindow{IP: 10, Wins: 2, QualityStarts: 1, ERA: 4}, 10)
	if math.Abs(pitcher.Wins-1.3) > .0001 || pitcher.QualityStarts != 1 || math.Abs(pitcher.ERA-3.3) > .0001 {
		t.Fatalf("pitcher=%#v", pitcher)
	}
	if got := NextPitcherWindow(&PitcherWindow{IP: 100, QualityStarts: 12}, nil, 10); got.QualityStarts != 0 {
		t.Fatalf("projection-only quality starts=%v", got.QualityStarts)
	}
}

func TestPercentileBlendAndShrinkRemainDeterministic(t *testing.T) {
	value, ok := Percentile([]float64{40, 10, 20, 30}, .60)
	if !ok || value != 28 {
		t.Fatalf("percentile=%v ok=%v", value, ok)
	}
	cw, pw := BlendWeights(7, true)
	if math.Abs(cw+pw-1) > .0001 || ShrinkStatcast(.4, 0, .3, 50) != .3 {
		t.Fatalf("weights=%v/%v shrink=%v", cw, pw, ShrinkStatcast(.4, 0, .3, 50))
	}
}

func TestWaiverEligibilitySeparatesRolesPositionsAndFallbackTier(t *testing.T) {
	id := int64(1)
	yahooID := int64(10)
	player := domain.StoredFantasyPlayer{YahooPlayerID: &yahooID, MLBAMID: &id, Role: "B", Positions: "1B,OF"}
	candidates := []domain.WaiverCandidate{{MLBAMID: 1, Role: "H", Positions: "1B,OF", PlateAppearances: 80}, {MLBAMID: 2, Role: "H", Positions: "1B", PlateAppearances: 20}, {MLBAMID: 3, Role: "H", Positions: "1B", PlateAppearances: 60}}
	if !WaiverEligible(player, "1B", candidates) {
		t.Fatal("qualified hitter rejected")
	}
	player.Status = "IL10"
	if WaiverEligible(player, "1B", candidates) {
		t.Fatal("injured hitter qualified")
	}
	player.Status = ""
	player.YahooPlayerID = nil
	if YahooPickupAvailable(player) {
		t.Fatal("non-Yahoo player entered the available fallback tier")
	}
	pitcherID := int64(20)
	pitcher := domain.StoredFantasyPlayer{YahooPlayerID: &yahooID, MLBAMID: &pitcherID, Role: "P", Positions: "SP"}
	pitchers := []domain.WaiverCandidate{{MLBAMID: 20, Role: "P", Positions: "SP", InningsPitched: 80, Games: 20, GamesStarted: 10}, {MLBAMID: 21, Role: "P", Positions: "SP", InningsPitched: 40, Games: 20, GamesStarted: 10}, {MLBAMID: 22, Role: "P", Positions: "RP", InningsPitched: 70, Games: 40, GamesStarted: 0}}
	if !WaiverEligible(pitcher, "", pitchers) {
		t.Fatal("qualified starter was mixed with the reliever opportunity pool")
	}
}
