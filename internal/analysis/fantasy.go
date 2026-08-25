// Package analysis contains deterministic fantasy-player scoring helpers.
package analysis

import (
	"math"
	"slices"
	"strings"

	"github.com/queone/skout/internal/domain"
)

// PitcherRole is the usage class used by waiver thresholds and projection windows.
type PitcherRole uint8

const (
	Starter PitcherRole = iota
	Reliever
)

// RecentAppearance carries the role evidence needed from one recent game.
type RecentAppearance struct{ GamesStarted int64 }

// ClassifyPitcher uses probable status, recent starts, season use, then eligibility.
func ClassifyPitcher(positions string, probable bool, recent []RecentAppearance, seasonGames, seasonStarts float64) PitcherRole {
	starts := 0
	for _, appearance := range recent[:min(5, len(recent))] {
		if appearance.GamesStarted > 0 {
			starts++
		}
	}
	if probable || starts >= 3 {
		return Starter
	}
	if seasonGames > 0 {
		ratio := seasonStarts / seasonGames
		if ratio >= .6 {
			return Starter
		}
		if ratio < .35 {
			return Reliever
		}
	}
	return ClassifyPitcherPositions(positions)
}

// ClassifyPitcherPositions falls back to exact eligible-position labels.
func ClassifyPitcherPositions(positions string) PitcherRole {
	for position := range strings.SplitSeq(positions, ",") {
		if strings.TrimSpace(position) == "SP" {
			return Starter
		}
	}
	return Reliever
}

// BlendWeights returns current and prior-season weights for league progress.
func BlendWeights(games int64, hasPrior bool) (float64, float64) {
	if !hasPrior {
		return 1, 0
	}
	switch {
	case games <= 7:
		return .15 / .95, .80 / .95
	case games <= 14:
		return .25, .75
	case games <= 27:
		return .5, .5
	default:
		return 1, 0
	}
}

// DampenOpportunity shifts current-season weight toward prior data at small samples.
func DampenOpportunity(current, prior, opportunity float64, pitcher bool) (float64, float64) {
	if prior == 0 {
		return current, prior
	}
	target := 150.0
	if pitcher {
		target = 40
	}
	ramp := max(0, min(1, opportunity/target))
	dampened := current * ramp
	return dampened, prior + current - dampened
}

// BlendValue applies the scheduled current/prior weights.
func BlendValue(current float64, prior *float64, games int64) float64 {
	cw, pw := BlendWeights(games, prior != nil)
	if prior == nil {
		return current * cw
	}
	return current*cw + *prior*pw
}

// ShrinkStatcast applies empirical-Bayes shrinkage toward a pool mean.
func ShrinkStatcast(raw, sample, mean, k float64) float64 {
	if sample <= 0 {
		return mean
	}
	return (raw*sample + mean*k) / (sample + k)
}

// Percentile returns the R-7 interpolated percentile.
func Percentile(values []float64, percentile float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	values = append([]float64(nil), values...)
	slices.Sort(values)
	if len(values) == 1 {
		return values[0], true
	}
	p := max(0, min(1, percentile))
	h := float64(len(values)-1) * p
	lo, hi := int(math.Floor(h)), int(math.Ceil(h))
	return values[lo] + (values[hi]-values[lo])*(h-float64(lo)), true
}

// HitterWindow is one projection or recent-use batting window.
type HitterWindow struct {
	PA, Runs, HomeRuns, RBI, StolenBases, Average, OBP, OPS float64
}

// PitcherWindow is one projection or recent-use pitching window.
type PitcherWindow struct {
	IP, QualityStarts, Wins, Saves, Strikeouts, ERA, WHIP float64
}

// NextHitterWindow scales and blends a full projection with recent results.
func NextHitterWindow(projection, recent *HitterWindow, plateAppearances float64) HitterWindow {
	if plateAppearances <= 0 {
		return HitterWindow{}
	}
	scale := func(input *HitterWindow) *HitterWindow {
		if input == nil || input.PA <= 0 {
			return nil
		}
		ratio := plateAppearances / input.PA
		return &HitterWindow{plateAppearances, input.Runs * ratio, input.HomeRuns * ratio, input.RBI * ratio, input.StolenBases * ratio, input.Average, input.OBP, input.OPS}
	}
	p, r := scale(projection), scale(recent)
	if p == nil && r == nil {
		return HitterWindow{}
	}
	if p == nil {
		return *r
	}
	if r == nil {
		return *p
	}
	return HitterWindow{plateAppearances, windowBlend(p.Runs, r.Runs), windowBlend(p.HomeRuns, r.HomeRuns), windowBlend(p.RBI, r.RBI), windowBlend(p.StolenBases, r.StolenBases), windowBlend(p.Average, r.Average), windowBlend(p.OBP, r.OBP), windowBlend(p.OPS, r.OPS)}
}

// NextPitcherWindow scales and blends a full projection while retaining recent QS.
func NextPitcherWindow(projection, recent *PitcherWindow, innings float64) PitcherWindow {
	if innings <= 0 {
		return PitcherWindow{}
	}
	scale := func(input *PitcherWindow) *PitcherWindow {
		if input == nil || input.IP <= 0 {
			return nil
		}
		ratio := innings / input.IP
		return &PitcherWindow{innings, input.QualityStarts * ratio, input.Wins * ratio, input.Saves * ratio, input.Strikeouts * ratio, input.ERA, input.WHIP}
	}
	p, r := scale(projection), scale(recent)
	if p == nil && r == nil {
		return PitcherWindow{}
	}
	if p == nil {
		return *r
	}
	if r == nil {
		p.QualityStarts = 0
		return *p
	}
	return PitcherWindow{innings, r.QualityStarts, windowBlend(p.Wins, r.Wins), windowBlend(p.Saves, r.Saves), windowBlend(p.Strikeouts, r.Strikeouts), windowBlend(p.ERA, r.ERA), windowBlend(p.WHIP, r.WHIP)}
}

func windowBlend(projection, recent float64) float64 { return projection*.70 + recent*.30 }

// YahooPickupAvailable reports whether Yahoo identifies a player as unowned.
func YahooPickupAvailable(player domain.StoredFantasyPlayer) bool {
	return player.YahooPlayerID != nil && player.Owner == nil
}

// WaiverEligible applies active-roster, identity, injury, role, and opportunity gates.
func WaiverEligible(player domain.StoredFantasyPlayer, requestedPosition string, candidates []domain.WaiverCandidate) bool {
	if player.Owner != nil || strings.HasPrefix(player.Status, "IL") || strings.EqualFold(player.Status, "NA") || strings.EqualFold(player.Status, "SUSP") || player.MLBAMID == nil {
		return false
	}
	if player.Role == "B" {
		var candidate *domain.WaiverCandidate
		for index := range candidates {
			if candidates[index].MLBAMID == *player.MLBAMID && candidates[index].Role == "H" {
				candidate = &candidates[index]
				break
			}
		}
		if candidate == nil {
			return false
		}
		positions := hitterPositions(player.Positions)
		if requestedPosition != "" {
			positions = []string{requestedPosition}
		}
		floor := math.Inf(1)
		for _, position := range positions {
			var values []float64
			for _, value := range candidates {
				if value.Role == "H" && containsPosition(value.Positions, position) && value.PlateAppearances > 0 {
					values = append(values, value.PlateAppearances)
				}
			}
			if value, ok := Percentile(values, .60); ok {
				floor = min(floor, value)
			}
		}
		return candidate.PlateAppearances >= floor
	}
	var candidate *domain.WaiverCandidate
	for index := range candidates {
		if candidates[index].MLBAMID == *player.MLBAMID && candidates[index].Role == "P" {
			candidate = &candidates[index]
			break
		}
	}
	if candidate == nil {
		return false
	}
	starter := ClassifyPitcher(candidate.Positions, false, nil, float64(candidate.Games), float64(candidate.GamesStarted)) == Starter
	var values []float64
	for _, value := range candidates {
		valueStarter := ClassifyPitcher(value.Positions, false, nil, float64(value.Games), float64(value.GamesStarted)) == Starter
		if value.Role == "P" && valueStarter == starter && value.InningsPitched > 0 {
			values = append(values, value.InningsPitched)
		}
	}
	floor, ok := Percentile(values, .60)
	return ok && candidate.InningsPitched >= floor
}

func hitterPositions(positions string) []string {
	var output []string
	for position := range strings.SplitSeq(positions, ",") {
		position = strings.TrimSpace(position)
		if slices.Contains([]string{"C", "1B", "2B", "3B", "SS", "OF"}, position) {
			output = append(output, position)
		}
	}
	return output
}

func containsPosition(positions, requested string) bool {
	for position := range strings.SplitSeq(positions, ",") {
		if strings.EqualFold(strings.TrimSpace(position), requested) {
			return true
		}
	}
	return false
}
