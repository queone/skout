package providers

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/transport"
)

const (
	savantTimeout   = 20 * time.Second
	savantBodyLimit = 16 * 1024 * 1024
)

var savantBattingHeaders = [][]string{
	{"player_id", "playerid", "id"},
	{"pa", "plate_appearances"},
	{"bbe", "batted_ball_events"},
	{"est_woba", "xwoba"},
	{"exit_velocity_avg", "avg_hit_speed", "ev"},
	{"brl_percent", "barrel_batted_rate", "barrel_pct"},
	{"hard_hit_percent", "hard_hit_pct"},
	{"k_percent", "strikeout_percent", "strikeout_pct"},
	{"bb_percent", "walk_percent", "walk_pct"},
	{"sprint_speed"},
	{"on_base_plus_slg", "ops"},
}

var savantPitchingHeaders = [][]string{
	{"player_id", "playerid", "id"},
	{"pa", "bf", "batters_faced"},
	{"bbe", "batted_ball_events"},
	{"ff_avg_speed", "fastball_velo", "fbv"},
	{"whiff_percent", "whiff_pct"},
	{"chase_percent", "oz_swing_percent", "ch_pct"},
	{"groundballs_percent", "gb_percent", "gb_pct"},
	{"k_percent", "strikeout_percent", "strikeout_pct"},
	{"bb_percent", "walk_percent", "walk_pct"},
}

// SavantEndpoints contains the validated public leaderboard root.
type SavantEndpoints struct{ Root *url.URL }

// NewSavantEndpoints validates an injected leaderboard root.
func NewSavantEndpoints(root string) (SavantEndpoints, error) {
	target, err := validatePublicEndpoint("configure Baseball Savant endpoint", "endpoint", root)
	if err != nil {
		return SavantEndpoints{}, err
	}
	return SavantEndpoints{Root: target}, nil
}

// ProductionSavantEndpoints returns the public Baseball Savant leaderboard.
func ProductionSavantEndpoints() SavantEndpoints {
	endpoints, _ := NewSavantEndpoints("https://baseballsavant.mlb.com/leaderboard/custom")
	return endpoints
}

// SavantRow is one normalized Baseball Savant season and role row.
type SavantRow struct {
	MLBAMID           int64
	Season            int64
	StatGroup         string
	PlateAppearances  int64
	BattedBallEvents  int64
	XWOBA             *float64
	ExitVeloAverage   *float64
	BarrelPercent     *float64
	HardHitPercent    *float64
	SprintSpeed       *float64
	StrikeoutPercent  *float64
	WalkPercent       *float64
	OPS               *float64
	FastballVelo      *float64
	WhiffPercent      *float64
	ChasePercent      *float64
	GroundBallPercent *float64
}

// SavantClient acquires public Baseball Savant CSV leaderboards.
type SavantClient struct {
	http      *transport.Client
	endpoints SavantEndpoints
}

func NewSavantClient(http *transport.Client, endpoints SavantEndpoints) *SavantClient {
	return &SavantClient{http: http, endpoints: endpoints}
}

func NewProductionSavantClient(http *transport.Client) *SavantClient {
	return NewSavantClient(http, ProductionSavantEndpoints())
}

func (client *SavantClient) FetchBatting(season int64) ([]SavantRow, error) {
	return client.fetch(season, "batter", "batting")
}

func (client *SavantClient) FetchPitching(season int64) ([]SavantRow, error) {
	return client.fetch(season, "pitcher", "pitching")
}

func (client *SavantClient) fetch(season int64, kind, group string) ([]SavantRow, error) {
	const operation = "fetch Baseball Savant leaderboard"
	if season < 2000 || season > 2200 {
		return nil, invalid(operation, "season is outside the supported range")
	}
	target := *client.endpoints.Root
	selections := "pa,bbe,xwoba,exit_velocity_avg,barrel_batted_rate,hard_hit_percent,k_percent,bb_percent,sprint_speed,on_base_plus_slg"
	if group == "pitching" {
		selections = "pa,bbe,ff_avg_speed,whiff_percent,oz_swing_percent,groundballs_percent,k_percent,bb_percent"
	}
	query := target.Query()
	query.Set("year", strconv.FormatInt(season, 10))
	query.Set("type", kind)
	query.Set("filter", "")
	query.Set("sort", "4")
	query.Set("sortDir", "desc")
	query.Set("min", "1")
	query.Set("selections", selections)
	query.Set("csv", "true")
	target.RawQuery = query.Encode()
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: target.String(), Timeout: savantTimeout, BodyLimit: savantBodyLimit})
	if err != nil {
		return nil, operationError(operation, "dispatch request", err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, invalid(operation, fmt.Sprintf("HTTP status %d", response.Status))
	}
	return ParseSavantCSV(response.Body, season, group)
}

// ParseSavantCSV parses one complete Baseball Savant response.
func ParseSavantCSV(payload []byte, season int64, group string) ([]SavantRow, error) {
	const operation = "parse Baseball Savant leaderboard"
	if season <= 0 || group != "batting" && group != "pitching" {
		return nil, invalid(operation, "positive season and batting or pitching stat group are required")
	}
	if !utf8.Valid(payload) {
		return nil, invalid(operation, "response is not UTF-8")
	}
	reader := csv.NewReader(strings.NewReader(string(payload)))
	reader.TrimLeadingSpace = true
	headers, err := reader.Read()
	if err == io.EOF {
		return nil, invalid(operation, "CSV header is absent")
	}
	if err != nil {
		return nil, operationError(operation, "decode CSV header", err)
	}
	index := make(map[string]int, len(headers))
	for offset, header := range headers {
		index[strings.ToLower(strings.TrimSpace(header))] = offset
	}
	required := savantBattingHeaders
	if group == "pitching" {
		required = savantPitchingHeaders
	}
	for _, aliases := range required {
		if _, ok := savantColumn(index, aliases...); !ok {
			return nil, invalid(operation, "CSV lacks required column "+aliases[0])
		}
	}
	var rows []SavantRow
	for line := 2; ; line++ {
		values, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, operationError(operation, fmt.Sprintf("decode CSV row %d", line), readErr)
		}
		if len(values) == 1 && strings.TrimSpace(values[0]) == "" {
			continue
		}
		if len(values) != len(headers) {
			return nil, invalid(operation, fmt.Sprintf("row %d has %d fields; expected %d", line, len(values), len(headers)))
		}
		integer := func(names ...string) int64 {
			value, _ := savantValue(values, index, names...)
			parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			return parsed
		}
		number := func(names ...string) *float64 {
			value, ok := savantValue(values, index, names...)
			if !ok || strings.TrimSpace(value) == "" {
				return nil
			}
			parsed, parseErr := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "%"), 64)
			if parseErr != nil {
				return nil
			}
			return &parsed
		}
		row := SavantRow{
			MLBAMID:           integer("player_id", "playerid", "id"),
			Season:            season,
			StatGroup:         group,
			PlateAppearances:  integer("pa", "bf", "plate_appearances", "batters_faced"),
			BattedBallEvents:  integer("bbe", "batted_ball_events"),
			XWOBA:             number("est_woba", "xwoba"),
			ExitVeloAverage:   number("exit_velocity_avg", "avg_hit_speed", "ev"),
			BarrelPercent:     number("brl_percent", "barrel_batted_rate", "barrel_pct"),
			HardHitPercent:    number("hard_hit_percent", "hard_hit_pct"),
			SprintSpeed:       number("sprint_speed"),
			StrikeoutPercent:  number("k_percent", "strikeout_percent", "strikeout_pct"),
			WalkPercent:       number("bb_percent", "walk_percent", "walk_pct"),
			OPS:               number("on_base_plus_slg", "ops"),
			FastballVelo:      number("ff_avg_speed", "fastball_velo", "fbv"),
			WhiffPercent:      number("whiff_percent", "whiff_pct"),
			ChasePercent:      number("chase_percent", "oz_swing_percent", "ch_pct"),
			GroundBallPercent: number("groundballs_percent", "gb_percent", "gb_pct"),
		}
		if row.MLBAMID <= 0 {
			return nil, invalid(operation, fmt.Sprintf("row %d lacks a positive player id", line))
		}
		needsPA := group == "batting" && row.XWOBA != nil || group == "pitching" && (row.FastballVelo != nil || row.WhiffPercent != nil || row.ChasePercent != nil)
		if needsPA && row.PlateAppearances <= 0 {
			return nil, invalid(operation, fmt.Sprintf("row %d lacks a required PA/BF denominator", line))
		}
		needsBBE := group == "batting" && (row.ExitVeloAverage != nil || row.BarrelPercent != nil || row.HardHitPercent != nil) || group == "pitching" && row.GroundBallPercent != nil
		if needsBBE && row.BattedBallEvents <= 0 {
			if group == "batting" {
				row.ExitVeloAverage, row.BarrelPercent, row.HardHitPercent = nil, nil, nil
			} else {
				row.GroundBallPercent = nil
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, invalid(operation, "CSV contains no player rows")
	}
	return rows, nil
}

func savantColumn(index map[string]int, names ...string) (int, bool) {
	for _, name := range names {
		if offset, ok := index[name]; ok {
			return offset, true
		}
	}
	return 0, false
}

func savantValue(values []string, index map[string]int, names ...string) (string, bool) {
	offset, ok := savantColumn(index, names...)
	if !ok || offset >= len(values) {
		return "", false
	}
	return values[offset], true
}
