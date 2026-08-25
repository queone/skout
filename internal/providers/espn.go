package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/queone/skout/internal/transport"
)

const (
	espnTimeout   = 10 * time.Second
	espnBodyLimit = 4 * 1024 * 1024
)

// ESPNEndpoints contains validated scoreboard and odds roots.
type ESPNEndpoints struct {
	Scoreboard *url.URL
	Odds       *url.URL
}

func NewESPNEndpoints(scoreboard, odds string) (ESPNEndpoints, error) {
	first, err := validatePublicEndpoint("configure ESPN endpoints", "scoreboard", scoreboard)
	if err != nil {
		return ESPNEndpoints{}, err
	}
	second, err := validatePublicEndpoint("configure ESPN endpoints", "odds", odds)
	if err != nil {
		return ESPNEndpoints{}, err
	}
	return ESPNEndpoints{Scoreboard: first, Odds: second}, nil
}

func ProductionESPNEndpoints() ESPNEndpoints {
	endpoints, _ := NewESPNEndpoints(
		"https://site.api.espn.com/apis/site/v2/sports/baseball/mlb/scoreboard",
		"https://sports.core.api.espn.com/v2/sports/baseball/leagues/mlb/",
	)
	return endpoints
}

// ESPNGameLine is one normalized game and optional top-provider moneyline.
type ESPNGameLine struct {
	EventID       string `json:"event_id"`
	CompetitionID string `json:"competition_id"`
	HomeTeam      string `json:"home_team"`
	AwayTeam      string `json:"away_team"`
	Sportsbook    string `json:"sportsbook"`
	HomeMoneyline int64  `json:"home_moneyline"`
	AwayMoneyline int64  `json:"away_moneyline"`
	Quoted        bool   `json:"quoted"`
}

// ESPNOddsIssue is one bounded degraded per-game odds result.
type ESPNOddsIssue struct {
	EventID string `json:"event_id"`
	Detail  string `json:"detail"`
}

// ESPNSlateLines is a complete two-day acquisition result.
type ESPNSlateLines struct {
	Games  []ESPNGameLine  `json:"games"`
	Issues []ESPNOddsIssue `json:"issues"`
}

// ESPNClient acquires public scoreboard and odds data.
type ESPNClient struct {
	http      *transport.Client
	endpoints ESPNEndpoints
	version   string
}

func NewESPNClient(http *transport.Client, endpoints ESPNEndpoints, version string) *ESPNClient {
	return &ESPNClient{http: http, endpoints: endpoints, version: version}
}
func NewProductionESPNClient(http *transport.Client, version string) *ESPNClient {
	return NewESPNClient(http, ProductionESPNEndpoints(), version)
}

// FetchGameLines fetches the supplied UTC day and following UTC day.
func (client *ESPNClient) FetchGameLines(day time.Time) (ESPNSlateLines, error) {
	if day.Before(time.Unix(0, 0)) {
		return ESPNSlateLines{}, invalid("fetch ESPN game lines", "supplied day precedes the Unix epoch")
	}
	dates := []string{day.UTC().Format("20060102"), day.UTC().AddDate(0, 0, 1).Format("20060102")}
	type eventIdentity struct{ eventID, competitionID, home, away string }
	seen := map[string]struct{}{}
	var events []eventIdentity
	for _, date := range dates {
		target := *client.endpoints.Scoreboard
		query := target.Query()
		query.Set("dates", date)
		target.RawQuery = query.Encode()
		var response struct {
			Events []struct {
				ID           string `json:"id"`
				Competitions []struct {
					ID          string `json:"id"`
					Competitors []struct {
						HomeAway string `json:"homeAway"`
						Team     struct {
							DisplayName string `json:"displayName"`
						} `json:"team"`
					} `json:"competitors"`
				} `json:"competitions"`
			} `json:"events"`
		}
		if err := client.getJSON("fetch ESPN scoreboard", &target, &response); err != nil {
			return ESPNSlateLines{}, err
		}
		for _, event := range response.Events {
			if strings.TrimSpace(event.ID) == "" {
				continue
			}
			if _, exists := seen[event.ID]; exists {
				continue
			}
			seen[event.ID] = struct{}{}
			if len(event.Competitions) == 0 {
				continue
			}
			competition := event.Competitions[0]
			if strings.TrimSpace(competition.ID) == "" {
				continue
			}
			var home, away string
			for _, competitor := range competition.Competitors {
				switch competitor.HomeAway {
				case "home":
					home = competitor.Team.DisplayName
				case "away":
					away = competitor.Team.DisplayName
				}
			}
			if strings.TrimSpace(home) == "" || strings.TrimSpace(away) == "" {
				continue
			}
			events = append(events, eventIdentity{event.ID, competition.ID, home, away})
		}
	}
	result := ESPNSlateLines{Games: make([]ESPNGameLine, 0, len(events))}
	for _, event := range events {
		line := ESPNGameLine{EventID: event.eventID, CompetitionID: event.competitionID, HomeTeam: event.home, AwayTeam: event.away}
		joined := strings.TrimSuffix(client.endpoints.Odds.String(), "/") + "/events/" + url.PathEscape(event.eventID) + "/competitions/" + url.PathEscape(event.competitionID) + "/odds"
		target, err := url.Parse(joined)
		if err != nil {
			result.Issues = append(result.Issues, ESPNOddsIssue{event.eventID, bounded(err.Error(), 256)})
			result.Games = append(result.Games, line)
			continue
		}
		var odds struct {
			Items []struct {
				Provider struct {
					Name string `json:"name"`
				} `json:"provider"`
				Home struct {
					MoneyLine int64 `json:"moneyLine"`
				} `json:"homeTeamOdds"`
				Away struct {
					MoneyLine int64 `json:"moneyLine"`
				} `json:"awayTeamOdds"`
			} `json:"items"`
		}
		if err := client.getJSON("fetch ESPN odds", target, &odds); err != nil {
			result.Issues = append(result.Issues, ESPNOddsIssue{event.eventID, bounded(err.Error(), 256)})
		} else if len(odds.Items) > 0 {
			line.Sportsbook = odds.Items[0].Provider.Name
			line.HomeMoneyline = odds.Items[0].Home.MoneyLine
			line.AwayMoneyline = odds.Items[0].Away.MoneyLine
			line.Quoted = line.HomeMoneyline != 0 || line.AwayMoneyline != 0
		}
		result.Games = append(result.Games, line)
	}
	return result, nil
}

func (client *ESPNClient) getJSON(operation string, target *url.URL, output any) error {
	response, err := client.http.Execute(transport.Request{
		Method: transport.Get, URL: target.String(), Timeout: espnTimeout, BodyLimit: espnBodyLimit,
		Headers: []transport.Header{{Name: "User-Agent", Value: fmt.Sprintf("skout/%s (+https://github.com/queone/skout)", client.version)}, {Name: "Accept", Value: "application/json"}},
	})
	if err != nil {
		return operationError(operation, "request failed", err)
	}
	if response.Status != 200 {
		return invalid(operation, fmt.Sprintf("provider returned HTTP %d", response.Status))
	}
	if err := json.Unmarshal(response.Body, output); err != nil {
		return operationError(operation, "decode JSON response", err)
	}
	return nil
}

// MatchesTeam compares names through punctuation-insensitive folding.
func MatchesTeam(left, right string) bool { return foldTeam(left) == foldTeam(right) }

func foldTeam(value string) string {
	return strings.ToLower(strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return character
		}
		return -1
	}, value))
}

func validatePublicEndpoint(operation, label, value string) (*url.URL, error) {
	target, err := url.Parse(value)
	if err != nil || target.Host == "" {
		return nil, invalid(operation, label+" URL is invalid")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && endpointLoopback(target)) {
		return nil, invalid(operation, label+" URL must use HTTPS or loopback HTTP")
	}
	if target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return nil, invalid(operation, label+" URL must not contain credentials, a query, or a fragment")
	}
	return target, nil
}
