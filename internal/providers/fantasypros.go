package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/transport"
)

const (
	fantasyProsTimeout   = 20 * time.Second
	fantasyProsBodyLimit = 16 * 1024 * 1024
	FantasyProsMinimum   = 100
)

// ECRRow is one normalized FantasyPros expert-consensus rank.
type ECRRow struct {
	Name          string
	Team          string
	YahooPlayerID *int64
	Rank          int64
}

// FantasyProsEndpoints contains the validated public rankings page.
type FantasyProsEndpoints struct{ Rankings *url.URL }

func NewFantasyProsEndpoints(rankings string) (FantasyProsEndpoints, error) {
	target, err := validatePublicEndpoint("configure FantasyPros endpoint", "rankings", rankings)
	if err != nil {
		return FantasyProsEndpoints{}, err
	}
	return FantasyProsEndpoints{Rankings: target}, nil
}

func ProductionFantasyProsEndpoints() FantasyProsEndpoints {
	endpoints, _ := NewFantasyProsEndpoints("https://www.fantasypros.com/mlb/rankings/overall.php")
	return endpoints
}

type FantasyProsClient struct {
	http      *transport.Client
	endpoints FantasyProsEndpoints
}

func NewFantasyProsClient(http *transport.Client, endpoints FantasyProsEndpoints) *FantasyProsClient {
	return &FantasyProsClient{http: http, endpoints: endpoints}
}

func NewProductionFantasyProsClient(http *transport.Client) *FantasyProsClient {
	return NewFantasyProsClient(http, ProductionFantasyProsEndpoints())
}

// FetchECR fetches and parses the public FantasyPros overall rankings.
func (client *FantasyProsClient) FetchECR() ([]ECRRow, error) {
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: client.endpoints.Rankings.String(), Timeout: fantasyProsTimeout, BodyLimit: fantasyProsBodyLimit})
	if err != nil {
		return nil, operationError("fetch FantasyPros ECR", "dispatch request", err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, invalid("fetch FantasyPros ECR", fmt.Sprintf("HTTP status %d", response.Status))
	}
	if !utf8.Valid(response.Body) {
		return nil, invalid("parse FantasyPros ECR", "response is not UTF-8")
	}
	return ParseFantasyProsECR(string(response.Body))
}

// ParseFantasyProsECR extracts the balanced ecrData object from one page.
func ParseFantasyProsECR(body string) ([]ECRRow, error) {
	const operation = "parse FantasyPros ECR"
	marker := strings.Index(body, "var ecrData")
	if marker < 0 {
		return nil, invalid(operation, "ecrData marker is absent")
	}
	relativeOpen := strings.IndexByte(body[marker:], '{')
	if relativeOpen < 0 {
		return nil, invalid(operation, "opening object is absent")
	}
	open := marker + relativeOpen
	depth := 0
	quoted := false
	escaped := false
	end := -1
	for offset, character := range body[open:] {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quoted {
			escaped = true
			continue
		}
		if character == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch character {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = open + offset + 1
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, invalid(operation, "closing object is absent")
	}
	var envelope struct {
		Players []struct {
			Name          string          `json:"player_name"`
			Team          string          `json:"player_team_id"`
			YahooPlayerID json.RawMessage `json:"yahoo_player_id"`
			Rank          json.RawMessage `json:"rank_ecr"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(body[open:end]), &envelope); err != nil {
		return nil, operationError(operation, "decode ecrData", err)
	}
	output := make([]ECRRow, 0, len(envelope.Players))
	for offset, raw := range envelope.Players {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return nil, invalid(operation, fmt.Sprintf("player row %d lacks a name", offset+1))
		}
		rank, err := fantasyProsInt(raw.Rank)
		if err != nil || rank <= 0 {
			return nil, invalid(operation, fmt.Sprintf("player row %d lacks a positive ECR rank", offset+1))
		}
		yahooID, err := fantasyProsOptionalInt(raw.YahooPlayerID)
		if err != nil {
			return nil, invalid(operation, fmt.Sprintf("player row %d has an invalid Yahoo player id", offset+1))
		}
		output = append(output, ECRRow{Name: name, Team: strings.ToUpper(strings.TrimSpace(raw.Team)), YahooPlayerID: yahooID, Rank: rank})
	}
	if len(output) == 0 {
		return nil, invalid(operation, "ecrData contains no player rows")
	}
	sort.SliceStable(output, func(i, j int) bool {
		if output[i].Rank != output[j].Rank {
			return output[i].Rank < output[j].Rank
		}
		if output[i].Name != output[j].Name {
			return output[i].Name < output[j].Name
		}
		return output[i].Team < output[j].Team
	})
	return output, nil
}

// ValidateFantasyProsCompleteness enforces the frozen replacement threshold.
func ValidateFantasyProsCompleteness(rows []ECRRow) error {
	if len(rows) < FantasyProsMinimum {
		return invalid("validate FantasyPros ECR", "fewer than 100 rows")
	}
	return nil
}

func fantasyProsInt(raw json.RawMessage) (int64, error) {
	value, err := fantasyProsOptionalInt(raw)
	if err != nil || value == nil {
		return 0, fmt.Errorf("integer is absent or invalid")
	}
	return *value, nil
}

func fantasyProsOptionalInt(raw json.RawMessage) (*int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || text == `""` {
		return nil, nil
	}
	if strings.HasPrefix(text, "\"") {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
		text = strings.TrimSpace(decoded)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil, err
	}
	if value <= 0 {
		return nil, nil
	}
	return &value, nil
}
