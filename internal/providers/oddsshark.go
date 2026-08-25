package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/transport"
)

// OddsSharkEndpoints contains the validated public scores root.
type OddsSharkEndpoints struct{ Root *url.URL }

func NewOddsSharkEndpoints(root string) (OddsSharkEndpoints, error) {
	target, err := validatePublicEndpoint("configure OddsShark endpoint", "endpoint", root)
	if err != nil {
		return OddsSharkEndpoints{}, err
	}
	return OddsSharkEndpoints{Root: target}, nil
}

func ProductionOddsSharkEndpoints() OddsSharkEndpoints {
	endpoints, _ := NewOddsSharkEndpoints("https://www.oddsshark.com/api/scores/mlb")
	return endpoints
}

// OddsSharkGameLine is one future-game moneyline pair.
type OddsSharkGameLine struct {
	EventID       string `json:"event_id"`
	Date          string `json:"date"`
	StartTime     string `json:"start_time"`
	AwayTeam      string `json:"away_team"`
	HomeTeam      string `json:"home_team"`
	AwayMoneyline int64  `json:"away_moneyline"`
	HomeMoneyline int64  `json:"home_moneyline"`
}

// OddsSharkClient acquires public future lines.
type OddsSharkClient struct {
	http      *transport.Client
	endpoints OddsSharkEndpoints
}

func NewOddsSharkClient(http *transport.Client, endpoints OddsSharkEndpoints) *OddsSharkClient {
	return &OddsSharkClient{http: http, endpoints: endpoints}
}
func NewProductionOddsSharkClient(http *transport.Client) *OddsSharkClient {
	return NewOddsSharkClient(http, ProductionOddsSharkEndpoints())
}

// FetchGameLines fetches one ISO-date slate.
func (client *OddsSharkClient) FetchGameLines(date string) ([]OddsSharkGameLine, error) {
	if !frozenISODateShape(date) {
		return nil, invalid("fetch OddsShark MLB lines", "date must use YYYY-MM-DD")
	}
	target := *client.endpoints.Root
	query := target.Query()
	query.Set("date", date)
	target.RawQuery = query.Encode()
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: target.String(), Headers: []transport.Header{{Name: "Referer", Value: "https://www.oddsshark.com/mlb/scores"}}, Timeout: 10 * time.Second, BodyLimit: 4 * 1024 * 1024})
	if err != nil {
		return nil, operationError("fetch OddsShark MLB lines", "request failed", err)
	}
	if response.Status != 200 {
		return nil, invalid("fetch OddsShark MLB lines", fmt.Sprintf("provider returned HTTP %d", response.Status))
	}
	var value any
	if err := json.Unmarshal(response.Body, &value); err != nil {
		return nil, operationError("fetch OddsShark MLB lines", "decode JSON response", err)
	}
	games := collection(value)
	if games == nil {
		return nil, invalid("fetch OddsShark MLB lines", "game collection is absent")
	}
	var output []OddsSharkGameLine
	for _, game := range games {
		if line, ok := parseOddsSharkGame(game, date); ok {
			output = append(output, line)
		}
	}
	sort.Slice(output, func(i, j int) bool {
		left, right := output[i], output[j]
		if left.Date != right.Date {
			return left.Date < right.Date
		}
		if left.AwayTeam != right.AwayTeam {
			return left.AwayTeam < right.AwayTeam
		}
		if left.HomeTeam != right.HomeTeam {
			return left.HomeTeam < right.HomeTeam
		}
		return left.EventID < right.EventID
	})
	return output, nil
}

func collection(value any) []map[string]any {
	if values, ok := value.([]any); ok {
		return objectSlice(values)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"scores", "games"} {
		if values, ok := object[key].([]any); ok {
			return objectSlice(values)
		}
	}
	return nil
}

func objectSlice(values []any) []map[string]any {
	output := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			output = append(output, object)
		}
	}
	return output
}

func parseOddsSharkGame(value map[string]any, fallbackDate string) (OddsSharkGameLine, bool) {
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := value[key].(string); ok {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	number := func(keys ...string) int64 {
		for _, key := range keys {
			switch item := value[key].(type) {
			case float64:
				return int64(item)
			case string:
				parsed, _ := strconv.ParseInt(item, 10, 64)
				if parsed != 0 {
					return parsed
				}
			}
		}
		return 0
	}
	away, home := text("away_team", "awayTeam", "away_name", "awayName"), text("home_team", "homeTeam", "home_name", "homeName")
	awayLine, homeLine := number("away_moneyline", "awayMoneyLine", "away_ml", "awayPrice"), number("home_moneyline", "homeMoneyLine", "home_ml", "homePrice")
	if away == "" || home == "" || awayLine == 0 || homeLine == 0 {
		return OddsSharkGameLine{}, false
	}
	suppliedDate := text("date", "game_date", "gameDate")
	date := fallbackDate
	if supplied := []rune(suppliedDate); len(supplied) > 0 {
		date = strings.TrimSpace(string(supplied[:min(10, len(supplied))]))
		if date == "" {
			date = fallbackDate
		}
	}
	return OddsSharkGameLine{EventID: text("id", "event_id", "eventId"), Date: date, StartTime: suppliedDate, AwayTeam: away, HomeTeam: home, AwayMoneyline: awayLine, HomeMoneyline: homeLine}, true
}

func frozenISODateShape(date string) bool {
	if len(date) != 10 {
		return false
	}
	for index, character := range []byte(date) {
		if index == 4 || index == 7 {
			if character != '-' {
				return false
			}
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
