// Package app coordinates CLI commands across local and provider boundaries.
package app

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/queone/skout/internal/transport"
)

const (
	fetchTimeout   = 20 * time.Second
	fetchBodyLimit = 16 * 1024 * 1024
)

type fetchOrigin struct {
	base    string
	headers []transport.Header
}

// Fetch executes one origin-pinned public-provider request.
func Fetch(client *transport.Client, version, host, path string) (string, error) {
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("fetch: path must remain on the selected provider origin; correct the path and retry")
	}
	origins := fetchOrigins(version)
	origin, ok := origins[host]
	if !ok {
		return "", fmt.Errorf("fetch: unknown host %s; choose a documented provider and retry", host)
	}
	response, err := client.Execute(transport.Request{
		Method:    transport.Get,
		URL:       origin.base + "/" + strings.TrimLeft(path, "/"),
		Headers:   origin.headers,
		Timeout:   fetchTimeout,
		BodyLimit: fetchBodyLimit,
	})
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "HTTP %d\n", response.Status)
	for _, header := range response.Headers {
		fmt.Fprintf(&output, "%s: %s\n", header.Name, header.Value)
	}
	output.WriteByte('\n')
	output.WriteString(frozenLossyUTF8(response.Body))
	return output.String(), nil
}

func frozenLossyUTF8(input []byte) string {
	if utf8.Valid(input) {
		return string(input)
	}
	var output strings.Builder
	for len(input) > 0 {
		character, size := utf8.DecodeRune(input)
		if character != utf8.RuneError || size > 1 {
			output.Write(input[:size])
			input = input[size:]
			continue
		}
		output.WriteRune(utf8.RuneError)
		if !utf8.FullRune(input) {
			break
		}
		input = input[1:]
	}
	return output.String()
}

// FetchProduction executes Fetch through the production transport.
func FetchProduction(version, host, path string) (string, error) {
	return Fetch(transport.Production(), version, host, path)
}

func fetchOrigins(version string) map[string]fetchOrigin {
	return map[string]fetchOrigin{
		"mlb":         {base: "https://statsapi.mlb.com"},
		"espn":        {base: "https://site.api.espn.com", headers: []transport.Header{{Name: "User-Agent", Value: "skout/" + version + " (+https://github.com/queone/skout)"}, {Name: "Accept", Value: "application/json"}}},
		"oddsshark":   {base: "https://www.oddsshark.com", headers: []transport.Header{{Name: "Referer", Value: "https://www.oddsshark.com/mlb/scores"}}},
		"rotowire":    {base: "https://www.rotowire.com"},
		"savant":      {base: "https://baseballsavant.mlb.com"},
		"yahoo":       {base: "https://pub-api-ro.fantasysports.yahoo.com", headers: []transport.Header{{Name: "Accept", Value: "application/json"}}},
		"fangraphs":   {base: "https://www.fangraphs.com", headers: []transport.Header{{Name: "User-Agent", Value: "Mozilla/5.0 (compatible; skout) AppleWebKit/537.36"}, {Name: "Accept", Value: "text/html,application/xhtml+xml"}}},
		"fantasypros": {base: "https://www.fantasypros.com"},
	}
}
