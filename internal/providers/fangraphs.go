package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/transport"
)

const (
	fanGraphsTimeout   = 20 * time.Second
	fanGraphsBodyLimit = 16 * 1024 * 1024
	FanGraphsMinimum   = 100
)

// FanGraphsEndpoints contains the validated public leaderboard and projection roots.
type FanGraphsEndpoints struct {
	Leaderboard *url.URL
	Projections *url.URL
}

func NewFanGraphsEndpoints(leaderboard, projections string) (FanGraphsEndpoints, error) {
	first, err := validatePublicEndpoint("configure FanGraphs endpoints", "leaderboard", leaderboard)
	if err != nil {
		return FanGraphsEndpoints{}, err
	}
	second, err := validatePublicEndpoint("configure FanGraphs endpoints", "projections", projections)
	if err != nil {
		return FanGraphsEndpoints{}, err
	}
	return FanGraphsEndpoints{Leaderboard: first, Projections: second}, nil
}

func ProductionFanGraphsEndpoints() FanGraphsEndpoints {
	endpoints, _ := NewFanGraphsEndpoints(
		"https://www.fangraphs.com/api/leaders/major-league/data",
		"https://www.fangraphs.com/api/projections",
	)
	return endpoints
}

// FanGraphsLeaderRow contains one leaderboard identity and batted-ball row.
type FanGraphsLeaderRow struct {
	FanGraphsID string
	MLBAMID     *int64
	FlyBallPct  float64
	HomeRunFB   float64
}

// FanGraphsProjectionRow is one source-native projection row.
type FanGraphsProjectionRow struct {
	FanGraphsID string
	MLBAMID     *int64
	PA          float64
	IP          float64
	HR          float64
	R           float64
	RBI         float64
	SB          float64
	AVG         float64
	OBP         float64
	SLG         float64
	ERA         float64
	WHIP        float64
	K           float64
	W           float64
	SV          float64
	BB          float64
}

// FanGraphsProjectionInput is one MLB-resolved source and role projection.
type FanGraphsProjectionInput struct {
	MLBAMID   int64
	Season    int64
	Source    string
	StatGroup string
	PA        float64
	IP        float64
	HR        float64
	R         float64
	RBI       float64
	SB        float64
	AVG       float64
	OBP       float64
	SLG       float64
	ERA       float64
	WHIP      float64
	K         float64
	W         float64
	SV        float64
	BB        float64
}

// FanGraphsBattedBallInput is one MLB-resolved leaderboard row.
type FanGraphsBattedBallInput struct {
	MLBAMID    int64
	Season     int64
	FlyBallPct float64
	HomeRunFB  float64
}

// FanGraphsSnapshot contains every FanGraphs-owned input for one season.
type FanGraphsSnapshot struct {
	Projections []FanGraphsProjectionInput
	BattedBall  []FanGraphsBattedBallInput
}

type FanGraphsClient struct {
	http      *transport.Client
	endpoints FanGraphsEndpoints
}

func NewFanGraphsClient(http *transport.Client, endpoints FanGraphsEndpoints) *FanGraphsClient {
	return &FanGraphsClient{http: http, endpoints: endpoints}
}

func NewProductionFanGraphsClient(http *transport.Client) *FanGraphsClient {
	return NewFanGraphsClient(http, ProductionFanGraphsEndpoints())
}

// FetchLeaderboard fetches one public MLB leaderboard.
func (client *FanGraphsClient) FetchLeaderboard(season int64) ([]FanGraphsLeaderRow, error) {
	if err := validateFanGraphsSeason(season); err != nil {
		return nil, err
	}
	target := *client.endpoints.Leaderboard
	query := target.Query()
	query.Set("pos", "all")
	query.Set("stats", "bat")
	query.Set("lg", "all")
	query.Set("qual", "0")
	query.Set("season", strconv.FormatInt(season, 10))
	query.Set("season1", strconv.FormatInt(season, 10))
	query.Set("type", "8")
	query.Set("month", "0")
	query.Set("pageItems", "2000")
	query.Set("ind", "0")
	target.RawQuery = query.Encode()
	payload, err := client.fetchJSON(target.String())
	if err != nil {
		return nil, err
	}
	return ParseFanGraphsLeaderboard(payload)
}

// FetchProjections fetches one supported projection system and role.
func (client *FanGraphsClient) FetchProjections(season int64, system, group string) ([]FanGraphsProjectionRow, error) {
	if err := validateFanGraphsSeason(season); err != nil {
		return nil, err
	}
	if system != "steamer" && system != "zips" && system != "atc" {
		return nil, invalid("fetch FanGraphs projections", "system must be steamer, zips, or atc")
	}
	wireGroup := ""
	switch group {
	case "batting":
		wireGroup = "bat"
	case "pitching":
		wireGroup = "pit"
	default:
		return nil, invalid("fetch FanGraphs projections", "stat group must be batting or pitching")
	}
	target := *client.endpoints.Projections
	query := target.Query()
	query.Set("type", system)
	query.Set("stats", wireGroup)
	query.Set("pos", "all")
	query.Set("season", strconv.FormatInt(season, 10))
	query.Set("sortstat", "ADP")
	query.Set("sortorder", "desc")
	query.Set("page", "1_5000")
	target.RawQuery = query.Encode()
	payload, err := client.fetchJSON(target.String())
	if err != nil {
		return nil, err
	}
	return ParseFanGraphsProjections(payload)
}

// FetchSnapshot fetches and MLB-resolves every frozen projection system.
func (client *FanGraphsClient) FetchSnapshot(season int64) (FanGraphsSnapshot, error) {
	leaders, err := client.FetchLeaderboard(season)
	if err != nil {
		return FanGraphsSnapshot{}, err
	}
	if len(leaders) < FanGraphsMinimum {
		return FanGraphsSnapshot{}, invalid("validate FanGraphs leaderboard", "fewer than 100 rows")
	}
	crosswalk := make(map[string]int64, len(leaders))
	batted := make([]FanGraphsBattedBallInput, 0, len(leaders))
	for _, row := range leaders {
		if row.MLBAMID == nil || *row.MLBAMID <= 0 {
			continue
		}
		crosswalk[row.FanGraphsID] = *row.MLBAMID
		batted = append(batted, FanGraphsBattedBallInput{MLBAMID: *row.MLBAMID, Season: season, FlyBallPct: row.FlyBallPct, HomeRunFB: row.HomeRunFB})
	}
	var projections []FanGraphsProjectionInput
	for _, system := range []string{"steamer", "zips", "atc"} {
		for _, group := range []string{"batting", "pitching"} {
			rows, fetchErr := client.FetchProjections(season, system, group)
			if fetchErr != nil {
				return FanGraphsSnapshot{}, fetchErr
			}
			for _, row := range rows {
				resolved := ResolveFanGraphsMLBAMID(row.MLBAMID, row.FanGraphsID, crosswalk)
				if resolved == nil || *resolved <= 0 {
					continue
				}
				projections = append(projections, FanGraphsProjectionInput{MLBAMID: *resolved, Season: season, Source: system, StatGroup: group, PA: row.PA, IP: row.IP, HR: row.HR, R: row.R, RBI: row.RBI, SB: row.SB, AVG: row.AVG, OBP: row.OBP, SLG: row.SLG, ERA: row.ERA, WHIP: row.WHIP, K: row.K, W: row.W, SV: row.SV, BB: row.BB})
			}
		}
	}
	if len(projections) < FanGraphsMinimum {
		return FanGraphsSnapshot{}, invalid("validate FanGraphs projections", "fewer than 100 resolved rows")
	}
	sort.Slice(batted, func(i, j int) bool { return batted[i].MLBAMID < batted[j].MLBAMID })
	sort.SliceStable(projections, func(i, j int) bool {
		left, right := projections[i], projections[j]
		if left.MLBAMID != right.MLBAMID {
			return left.MLBAMID < right.MLBAMID
		}
		if left.StatGroup != right.StatGroup {
			return left.StatGroup < right.StatGroup
		}
		return fanGraphsSourceOrder(left.Source) < fanGraphsSourceOrder(right.Source)
	})
	return FanGraphsSnapshot{Projections: projections, BattedBall: batted}, nil
}

// ResolveFanGraphsMLBAMID prefers the projection row's identity and falls back to the leaderboard crosswalk.
func ResolveFanGraphsMLBAMID(own *int64, fanGraphsID string, crosswalk map[string]int64) *int64 {
	if own != nil {
		value := *own
		return &value
	}
	if value, ok := crosswalk[fanGraphsID]; ok {
		resolved := value
		return &resolved
	}
	return nil
}

func (client *FanGraphsClient) fetchJSON(target string) ([]byte, error) {
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: target, Headers: []transport.Header{{Name: "Accept", Value: "application/json"}}, Timeout: fanGraphsTimeout, BodyLimit: fanGraphsBodyLimit})
	if err != nil {
		return nil, operationError("fetch FanGraphs data", "dispatch request", err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, invalid("fetch FanGraphs data", fmt.Sprintf("HTTP status %d", response.Status))
	}
	return response.Body, nil
}

// ParseFanGraphsLeaderboard normalizes a bare or data-wrapped response.
func ParseFanGraphsLeaderboard(payload []byte) ([]FanGraphsLeaderRow, error) {
	rows, err := fanGraphsRows(payload)
	if err != nil {
		return nil, err
	}
	var wire []struct {
		PlayerID json.RawMessage `json:"playerid"`
		MLBAMID  json.RawMessage `json:"xMLBAMID"`
		FBPct    float64         `json:"FB%"`
		HRFB     float64         `json:"HR/FB"`
	}
	if err := json.Unmarshal(rows, &wire); err != nil {
		return nil, operationError("parse FanGraphs JSON", "decode leaderboard rows", err)
	}
	output := make([]FanGraphsLeaderRow, 0, len(wire))
	for offset, row := range wire {
		id, idErr := fanGraphsID(row.PlayerID)
		if idErr != nil {
			return nil, invalid("parse FanGraphs JSON", fmt.Sprintf("leaderboard row %d has invalid playerid", offset+1))
		}
		mlbam, mlbamErr := fanGraphsOptionalInt(row.MLBAMID)
		if mlbamErr != nil {
			return nil, invalid("parse FanGraphs JSON", fmt.Sprintf("leaderboard row %d has invalid xMLBAMID", offset+1))
		}
		output = append(output, FanGraphsLeaderRow{FanGraphsID: id, MLBAMID: mlbam, FlyBallPct: row.FBPct, HomeRunFB: row.HRFB})
	}
	return output, nil
}

// ParseFanGraphsProjections normalizes a bare or data-wrapped response.
func ParseFanGraphsProjections(payload []byte) ([]FanGraphsProjectionRow, error) {
	rows, err := fanGraphsRows(payload)
	if err != nil {
		return nil, err
	}
	var wire []struct {
		PlayerID json.RawMessage `json:"playerid"`
		MLBAMID  json.RawMessage `json:"xMLBAMID"`
		PA       float64         `json:"PA"`
		IP       float64         `json:"IP"`
		HR       float64         `json:"HR"`
		R        float64         `json:"R"`
		RBI      float64         `json:"RBI"`
		SB       float64         `json:"SB"`
		AVG      float64         `json:"AVG"`
		OBP      float64         `json:"OBP"`
		SLG      float64         `json:"SLG"`
		ERA      float64         `json:"ERA"`
		WHIP     float64         `json:"WHIP"`
		K        float64         `json:"SO"`
		W        float64         `json:"W"`
		SV       float64         `json:"SV"`
		BB       float64         `json:"BB"`
	}
	if err := json.Unmarshal(rows, &wire); err != nil {
		return nil, operationError("parse FanGraphs JSON", "decode projection rows", err)
	}
	output := make([]FanGraphsProjectionRow, 0, len(wire))
	for offset, row := range wire {
		id, idErr := fanGraphsID(row.PlayerID)
		if idErr != nil {
			return nil, invalid("parse FanGraphs JSON", fmt.Sprintf("projection row %d has invalid playerid", offset+1))
		}
		mlbam, mlbamErr := fanGraphsOptionalInt(row.MLBAMID)
		if mlbamErr != nil {
			return nil, invalid("parse FanGraphs JSON", fmt.Sprintf("projection row %d has invalid xMLBAMID", offset+1))
		}
		output = append(output, FanGraphsProjectionRow{FanGraphsID: id, MLBAMID: mlbam, PA: row.PA, IP: row.IP, HR: row.HR, R: row.R, RBI: row.RBI, SB: row.SB, AVG: row.AVG, OBP: row.OBP, SLG: row.SLG, ERA: row.ERA, WHIP: row.WHIP, K: row.K, W: row.W, SV: row.SV, BB: row.BB})
	}
	return output, nil
}

func fanGraphsRows(payload []byte) ([]byte, error) {
	var value json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, operationError("parse FanGraphs JSON", "decode response", err)
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil, invalid("parse FanGraphs JSON", "response is empty")
	}
	if trimmed[0] == '[' {
		return trimmed, nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil || len(bytes.TrimSpace(envelope.Data)) == 0 {
		return nil, invalid("parse FanGraphs JSON", "response is neither an array nor a data envelope")
	}
	if first := bytes.TrimSpace(envelope.Data); len(first) == 0 || first[0] != '[' {
		return nil, invalid("parse FanGraphs JSON", "data field is not an array")
	}
	return envelope.Data, nil
}

func fanGraphsID(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", fmt.Errorf("absent id")
	}
	if strings.HasPrefix(trimmed, "\"") {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("invalid id")
		}
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return "", err
	}
	if _, err := number.Int64(); err != nil {
		return "", err
	}
	return number.String(), nil
}

func fanGraphsOptionalInt(raw json.RawMessage) (*int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == `""` {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		trimmed = strings.TrimSpace(text)
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func validateFanGraphsSeason(season int64) error {
	if season < 2000 || season > 2200 {
		return invalid("fetch FanGraphs data", "season is outside the supported range")
	}
	return nil
}

func fanGraphsSourceOrder(source string) int {
	switch source {
	case "steamer":
		return 0
	case "zips":
		return 1
	case "atc":
		return 2
	default:
		return 3
	}
}
