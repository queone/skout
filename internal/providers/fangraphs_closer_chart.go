package providers

import (
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/transport"
)

const (
	fanGraphsCloserTimeout   = 20 * time.Second
	fanGraphsCloserBodyLimit = 16 * 1024 * 1024
	FanGraphsCloserMinimum   = 30
)

// CloserChartEntry is one complete FanGraphs closer-chart row.
type CloserChartEntry struct {
	Team string
	Name string
	Role string
}

// FanGraphsCloserEndpoints contains the validated public closer-chart URL.
type FanGraphsCloserEndpoints struct{ Chart *url.URL }

func NewFanGraphsCloserEndpoints(chart string) (FanGraphsCloserEndpoints, error) {
	target, err := validatePublicEndpoint("configure FanGraphs closer endpoint", "chart", chart)
	if err != nil {
		return FanGraphsCloserEndpoints{}, err
	}
	return FanGraphsCloserEndpoints{Chart: target}, nil
}

func ProductionFanGraphsCloserEndpoints() FanGraphsCloserEndpoints {
	endpoints, _ := NewFanGraphsCloserEndpoints("https://www.fangraphs.com/roster-resource/closer-depth-chart")
	return endpoints
}

type FanGraphsCloserClient struct {
	http      *transport.Client
	endpoints FanGraphsCloserEndpoints
}

func NewFanGraphsCloserClient(http *transport.Client, endpoints FanGraphsCloserEndpoints) *FanGraphsCloserClient {
	return &FanGraphsCloserClient{http: http, endpoints: endpoints}
}

func NewProductionFanGraphsCloserClient(http *transport.Client) *FanGraphsCloserClient {
	return NewFanGraphsCloserClient(http, ProductionFanGraphsCloserEndpoints())
}

// FetchCloserChart fetches and parses the public FanGraphs chart.
func (client *FanGraphsCloserClient) FetchCloserChart() ([]CloserChartEntry, error) {
	response, err := client.http.Execute(transport.Request{
		Method: transport.Get,
		URL:    client.endpoints.Chart.String(),
		Headers: []transport.Header{
			{Name: "User-Agent", Value: "Mozilla/5.0 (compatible; skout) AppleWebKit/537.36"},
			{Name: "Accept", Value: "text/html,application/xhtml+xml"},
		},
		Timeout: fanGraphsCloserTimeout, BodyLimit: fanGraphsCloserBodyLimit,
	})
	if err != nil {
		return nil, operationError("fetch FanGraphs closer chart", "dispatch request", err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, invalid("fetch FanGraphs closer chart", fmt.Sprintf("HTTP status %d", response.Status))
	}
	if !utf8.Valid(response.Body) {
		return nil, invalid("parse FanGraphs closer chart", "response is not UTF-8")
	}
	return ParseFanGraphsCloserChart(string(response.Body))
}

// ParseFanGraphsCloserChart parses every complete closer-chart row.
func ParseFanGraphsCloserChart(body string) ([]CloserChartEntry, error) {
	var output []CloserChartEntry
	for _, row := range strings.Split(body, "<tr")[1:] {
		team, teamOK := closerCell(row, "TEAM")
		name, nameOK := closerCell(row, "PLAYER")
		role, roleOK := closerCell(row, "PROJECTED ROLE")
		if !teamOK || !nameOK || !roleOK {
			continue
		}
		output = append(output, CloserChartEntry{Team: NormalizeFanGraphsTeam(team), Name: name, Role: role})
	}
	if len(output) == 0 {
		return nil, invalid("parse FanGraphs closer chart", "no complete closer rows were found")
	}
	sort.SliceStable(output, func(i, j int) bool {
		if output[i].Team != output[j].Team {
			return output[i].Team < output[j].Team
		}
		if output[i].Role != output[j].Role {
			return output[i].Role < output[j].Role
		}
		return output[i].Name < output[j].Name
	})
	return output, nil
}

// ValidateFanGraphsCloserCoverage rejects a snapshot that does not cover all MLB teams.
func ValidateFanGraphsCloserCoverage(rows []CloserChartEntry) error {
	teams := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Team) != "" {
			teams[row.Team] = struct{}{}
		}
	}
	if len(teams) < FanGraphsCloserMinimum {
		return invalid("validate FanGraphs closer chart", "fewer than 30 teams")
	}
	return nil
}

// FanGraphsCloserCandidates retains roles that designate a current closer.
func FanGraphsCloserCandidates(rows []CloserChartEntry) []CloserChartEntry {
	output := make([]CloserChartEntry, 0, len(rows))
	for _, row := range rows {
		if row.Role == "Closer" || row.Role == "Co-Closer" || row.Role == "Closer Committee" {
			output = append(output, row)
		}
	}
	return output
}

// NormalizeFanGraphsTeam converts FanGraphs abbreviations to skout's MLB abbreviations.
func NormalizeFanGraphsTeam(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "WSN":
		return "WSH"
	case "SFG":
		return "SF"
	case "CHW":
		return "CWS"
	case "SDP":
		return "SD"
	case "TBR":
		return "TB"
	case "KCR":
		return "KC"
	default:
		return value
	}
}

func closerCell(row, stat string) (string, bool) {
	marker := `data-stat="` + stat + `"`
	offset := strings.Index(row, marker)
	if offset < 0 {
		return "", false
	}
	tail := row[offset+len(marker):]
	start := strings.IndexByte(tail, '>')
	if start < 0 {
		return "", false
	}
	tail = tail[start+1:]
	end := strings.Index(tail, "</td>")
	if end < 0 {
		return "", false
	}
	value := strings.TrimSpace(html.UnescapeString(stripHTMLTags(tail[:end])))
	return value, value != ""
}

func stripHTMLTags(value string) string {
	var output strings.Builder
	inTag := false
	for _, character := range value {
		switch character {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				output.WriteRune(character)
			}
		}
	}
	return output.String()
}
