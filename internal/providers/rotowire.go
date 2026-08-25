package providers

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/transport"
)

const (
	RotoWireTTL       = 2 * time.Minute
	rotoWireTimeout   = 15 * time.Second
	rotoWireBodyLimit = 2 * 1024 * 1024
)

// DailyLineup contains one ordered RotoWire game lineup.
type DailyLineup struct {
	AwayTeam    string   `json:"away_team"`
	HomeTeam    string   `json:"home_team"`
	Confirmed   bool     `json:"confirmed"`
	AwayPlayers []string `json:"away_players"`
	HomePlayers []string `json:"home_players"`
	AwayPitcher string   `json:"away_pitcher"`
	HomePitcher string   `json:"home_pitcher"`
}

// RotoWireEndpoints contains the validated public daily-lineup page.
type RotoWireEndpoints struct{ DailyLineups *url.URL }

func NewRotoWireEndpoints(dailyLineups string) (RotoWireEndpoints, error) {
	target, err := validatePublicEndpoint("configure RotoWire endpoint", "daily lineups", dailyLineups)
	if err != nil {
		return RotoWireEndpoints{}, err
	}
	return RotoWireEndpoints{DailyLineups: target}, nil
}

func ProductionRotoWireEndpoints() RotoWireEndpoints {
	endpoints, _ := NewRotoWireEndpoints("https://www.rotowire.com/baseball/daily-lineups.php")
	return endpoints
}

type RotoWireClient struct {
	http      *transport.Client
	endpoints RotoWireEndpoints
}

func NewRotoWireClient(http *transport.Client, endpoints RotoWireEndpoints) *RotoWireClient {
	return &RotoWireClient{http: http, endpoints: endpoints}
}

func NewProductionRotoWireClient(http *transport.Client) *RotoWireClient {
	return NewRotoWireClient(http, ProductionRotoWireEndpoints())
}

// FetchCached returns fresh cached lineups or fetches and short-caches the page.
func (client *RotoWireClient) FetchCached(disk *cache.Disk) ([]DailyLineup, error) {
	if disk == nil {
		return nil, invalid("fetch RotoWire lineups", "cache is unavailable")
	}
	if lookup, err := disk.Get("rotowire", "daily-lineups", RotoWireTTL); err == nil && lookup.State == cache.Hit {
		var cached []DailyLineup
		if json.Unmarshal(lookup.Entry.Payload, &cached) == nil && len(cached) > 0 {
			return cached, nil
		}
	}
	response, err := client.http.Execute(transport.Request{Method: transport.Get, URL: client.endpoints.DailyLineups.String(), Timeout: rotoWireTimeout, BodyLimit: rotoWireBodyLimit})
	if err != nil {
		return nil, operationError("fetch RotoWire lineups", "request daily lineups", err)
	}
	if response.Status != 200 {
		return nil, invalid("fetch RotoWire lineups", fmt.Sprintf("HTTP %d", response.Status))
	}
	if !utf8.Valid(response.Body) {
		return nil, invalid("fetch RotoWire lineups", "response is not UTF-8")
	}
	lineups := ParseRotoWireDailyLineups(string(response.Body))
	if len(lineups) == 0 {
		return nil, invalid("fetch RotoWire lineups", "response has no lineup boxes")
	}
	if payload, encodeErr := json.Marshal(lineups); encodeErr == nil {
		_ = disk.Put("rotowire", "daily-lineups", payload)
	}
	return lineups, nil
}

// ParseRotoWireDailyLineups parses every recognizable lineup box in page order.
func ParseRotoWireDailyLineups(page string) []DailyLineup {
	var starts []int
	for offset := 0; ; {
		found := strings.Index(page[offset:], "lineup__box")
		if found < 0 {
			break
		}
		starts = append(starts, offset+found)
		offset += found + len("lineup__box")
	}
	output := make([]DailyLineup, 0, len(starts))
	for index, start := range starts {
		end := len(page)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		block := page[start:end]
		away, awayOK := rotoWireTextForClass(block, "lineup__mteam is-visit")
		home, homeOK := rotoWireTextForClass(block, "lineup__mteam is-home")
		if !awayOK || !homeOK {
			continue
		}
		awayList := rotoWireList(block, "lineup__list is-visit")
		homeList := rotoWireList(block, "lineup__list is-home")
		output = append(output, DailyLineup{
			AwayTeam:    NormalizeRotoWireTeam(away),
			HomeTeam:    NormalizeRotoWireTeam(home),
			Confirmed:   strings.Contains(block, "lineup__status is-confirmed"),
			AwayPlayers: rotoWirePlayerNames(awayList),
			HomePlayers: rotoWirePlayerNames(homeList),
			AwayPitcher: rotoWireHighlightedName(awayList),
			HomePitcher: rotoWireHighlightedName(homeList),
		})
	}
	return output
}

func rotoWireList(block, class string) string {
	start := strings.Index(block, class)
	if start < 0 {
		return ""
	}
	tail := block[start:]
	if end := strings.Index(tail, "</ul>"); end >= 0 {
		return tail[:end]
	}
	return tail
}

func rotoWireHighlightedName(list string) string {
	start := strings.Index(list, "lineup__player-highlight-name")
	if start < 0 {
		return ""
	}
	name, _ := rotoWireAnchorText(list[start:])
	return name
}

func rotoWirePlayerNames(list string) []string {
	var output []string
	for _, row := range strings.Split(list, "<li")[1:] {
		class, ok := rotoWireAttribute(row, "class")
		if !ok || !rotoWireClass(class, "lineup__player") || rotoWireClass(class, "lineup__player-highlight") {
			continue
		}
		if name, ok := rotoWireAnchorText(row); ok {
			output = append(output, name)
		}
	}
	return output
}

func rotoWireTextForClass(block, class string) (string, bool) {
	start := strings.Index(block, class)
	if start < 0 {
		return "", false
	}
	tail := block[start:]
	open := strings.IndexByte(tail, '>')
	if open < 0 {
		return "", false
	}
	content := tail[open+1:]
	close := strings.IndexByte(content, '<')
	if close < 0 {
		return "", false
	}
	value := strings.TrimSpace(html.UnescapeString(content[:close]))
	return value, value != ""
}

func rotoWireAnchorText(value string) (string, bool) {
	start := strings.Index(value, "<a")
	if start < 0 {
		return "", false
	}
	tail := value[start:]
	open := strings.IndexByte(tail, '>')
	if open < 0 {
		return "", false
	}
	tail = tail[open+1:]
	close := strings.Index(tail, "</a>")
	if close < 0 {
		return "", false
	}
	value = strings.TrimSpace(html.UnescapeString(stripHTMLTags(tail[:close])))
	return value, value != ""
}

func rotoWireAttribute(value, name string) (string, bool) {
	marker := name + `="`
	start := strings.Index(value, marker)
	if start < 0 {
		return "", false
	}
	tail := value[start+len(marker):]
	end := strings.IndexByte(tail, '"')
	if end < 0 {
		return "", false
	}
	return tail[:end], true
}

func rotoWireClass(value, class string) bool {
	for _, candidate := range strings.Fields(value) {
		if candidate == class {
			return true
		}
	}
	return false
}

// NormalizeRotoWireTeam converts displayed club names to skout abbreviations.
func NormalizeRotoWireTeam(value string) string {
	name := strings.TrimSpace(strings.SplitN(value, "(", 2)[0])
	teams := map[string]string{
		"Angels": "LAA", "Astros": "HOU", "Athletics": "ATH", "Blue Jays": "TOR", "Braves": "ATL",
		"Brewers": "MIL", "Cardinals": "STL", "Cubs": "CHC", "D-backs": "AZ", "Diamondbacks": "AZ",
		"Dodgers": "LAD", "Giants": "SF", "Guardians": "CLE", "Mariners": "SEA", "Marlins": "MIA",
		"Mets": "NYM", "Nationals": "WSH", "Orioles": "BAL", "Padres": "SD", "Phillies": "PHI",
		"Pirates": "PIT", "Rangers": "TEX", "Rays": "TB", "Red Sox": "BOS", "Reds": "CIN",
		"Rockies": "COL", "Royals": "KC", "Tigers": "DET", "Twins": "MIN", "White Sox": "CWS",
		"Yankees": "NYY",
	}
	if abbreviation, ok := teams[name]; ok {
		return abbreviation
	}
	return name
}
