// Package glossary parses, searches, and renders the embedded skout glossary.
package glossary

import (
	"bufio"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/queone/skout/internal/terminal"
)

const expectedEntryCount = 113

//go:embed glossary.md
var embeddedSource string

// Entry is one parsed glossary definition.
type Entry struct {
	Key        string
	Term       string
	Class      string
	Aliases    []string
	Definition string
}

// LookupResult contains one exact match, multiple substring matches, or miss suggestions.
type LookupResult struct {
	Entry       *Entry
	Matches     []*Entry
	Suggestions []string
}

// EmbeddedEntries parses and validates the glossary embedded in the binary.
func EmbeddedEntries() ([]Entry, error) {
	return parseEmbedded(embeddedSource)
}

func parseEmbedded(source string) ([]Entry, error) {
	entries, err := parse(source)
	if err != nil {
		return nil, err
	}
	if len(entries) != expectedEntryCount {
		return nil, fmt.Errorf("glossary: expected %d baseline entries, found %d", expectedEntryCount, len(entries))
	}
	return entries, nil
}

func parse(source string) ([]Entry, error) {
	var entries []Entry
	var current *Entry
	var currentLine int
	var definitionLines []string
	keys := make(map[string]struct{})

	flush := func() error {
		if current == nil {
			return nil
		}
		current.Definition = strings.Join(definitionLines, " ")
		if current.Definition == "" {
			return fmt.Errorf("glossary line %d: entry %q has an empty definition", currentLine, current.Key)
		}
		entries = append(entries, *current)
		current = nil
		definitionLines = nil
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(source))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		rawLine := scanner.Text()
		if strings.HasPrefix(rawLine, "###") {
			if err := flush(); err != nil {
				return nil, err
			}
			term, key, class, err := parseHeading(rawLine, lineNumber)
			if err != nil {
				return nil, err
			}
			if _, duplicate := keys[key]; duplicate {
				return nil, fmt.Errorf("glossary line %d: duplicate entry key %q", lineNumber, key)
			}
			keys[key] = struct{}{}
			current = &Entry{Key: key, Term: term, Class: class}
			currentLine = lineNumber
			continue
		}

		if current == nil {
			continue
		}
		line := strings.TrimSpace(rawLine)
		if value, ok := strings.CutPrefix(line, "- **Aliases:**"); ok {
			for alias := range strings.SplitSeq(value, ",") {
				if alias = strings.TrimSpace(alias); alias != "" {
					current.Aliases = append(current.Aliases, alias)
				}
			}
			continue
		}
		if line == "" || line == "---" || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "- **") || strings.HasPrefix(line, "**Prompt:**") {
			continue
		}
		definitionLines = append(definitionLines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("glossary: read source: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

func parseHeading(line string, lineNumber int) (string, string, string, error) {
	malformed := func() (string, string, string, error) {
		return "", "", "", fmt.Errorf("glossary line %d: malformed entry heading", lineNumber)
	}
	body, ok := strings.CutPrefix(line, "### ")
	if !ok || !strings.HasSuffix(body, "]") {
		return malformed()
	}
	classStart := strings.LastIndex(body, " [")
	if classStart < 0 {
		return malformed()
	}
	class := strings.TrimSpace(body[classStart+2 : len(body)-1])
	left := body[:classStart]
	if !strings.HasSuffix(left, "`)") {
		return malformed()
	}
	left = strings.TrimSuffix(left, "`)")
	keyStart := strings.LastIndex(left, " (`")
	if keyStart < 0 {
		return malformed()
	}
	term := strings.TrimSpace(left[:keyStart])
	key := strings.TrimSpace(left[keyStart+3:])
	if term == "" || key == "" || class == "" {
		return "", "", "", fmt.Errorf("glossary line %d: entry term, key, and class must be non-empty", lineNumber)
	}
	return term, key, class, nil
}

// Lookup resolves exact matches before source-ordered substring matches.
func Lookup(entries []Entry, query string) LookupResult {
	query = strings.ToLower(strings.TrimSpace(query))
	for i := range entries {
		if strings.ToLower(entries[i].Key) == query {
			return LookupResult{Entry: &entries[i]}
		}
	}
	for i := range entries {
		if strings.ToLower(entries[i].Term) == query {
			return LookupResult{Entry: &entries[i]}
		}
	}
	for i := range entries {
		for _, alias := range entries[i].Aliases {
			if strings.ToLower(alias) == query {
				return LookupResult{Entry: &entries[i]}
			}
		}
	}

	var matches []*Entry
	for i := range entries {
		entry := &entries[i]
		matched := strings.Contains(strings.ToLower(entry.Key), query) || strings.Contains(strings.ToLower(entry.Term), query)
		if !matched {
			for _, alias := range entry.Aliases {
				if strings.Contains(strings.ToLower(alias), query) {
					matched = true
					break
				}
			}
		}
		if matched {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 1 {
		return LookupResult{Entry: matches[0]}
	}
	if len(matches) > 1 {
		return LookupResult{Matches: matches}
	}
	return LookupResult{Suggestions: SuggestKeys(entries, query, 3)}
}

// SuggestKeys returns the nearest keys by Unicode-code-point edit distance.
func SuggestKeys(entries []Entry, query string, limit int) []string {
	type candidate struct {
		distance int
		key      string
	}
	query = strings.ToLower(query)
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, candidate{
			distance: levenshtein([]rune(query), []rune(strings.ToLower(entry.Key))),
			key:      entry.Key,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].key < candidates[j].key
	})
	if limit > len(candidates) {
		limit = len(candidates)
	}
	if limit < 0 {
		limit = 0
	}
	result := make([]string, 0, limit)
	for _, item := range candidates[:limit] {
		result = append(result, item.key)
	}
	return result
}

func levenshtein(left, right []rune) int {
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for i := range previous {
		previous[i] = i
	}
	for leftIndex, leftRune := range left {
		current[0] = leftIndex + 1
		for rightIndex, rightRune := range right {
			substitution := 0
			if leftRune != rightRune {
				substitution = 1
			}
			current[rightIndex+1] = min(previous[rightIndex+1]+1, current[rightIndex]+1, previous[rightIndex]+substitution)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

// RenderEntry renders one entry in plain mode without a trailing newline.
func RenderEntry(entry *Entry) string {
	return RenderEntryWithMode(entry, terminal.Plain)
}

// RenderEntryWithMode renders one entry with the selected terminal roles.
func RenderEntryWithMode(entry *Entry, mode terminal.ColorMode) string {
	heading := fmt.Sprintf("%s (%s) [%s]", entry.Term, entry.Key, entry.Class)
	lines := []string{terminal.Heading(heading, mode)}
	if len(entry.Aliases) > 0 {
		lines = append(lines, terminal.Alias("Aliases: "+strings.Join(entry.Aliases, ", "), mode))
	}
	lines = append(lines, entry.Definition)
	return strings.Join(lines, "\n")
}

// RenderFull renders the complete glossary in deterministic plain-text order.
func RenderFull(entries []Entry) string {
	return RenderFullWithMode(entries, terminal.Plain)
}

// RenderFullWithMode renders the complete glossary with the selected terminal roles.
func RenderFullWithMode(entries []Entry, mode terminal.ColorMode) string {
	classRank := map[string]int{"baseball": 0, "fantasy": 1, "skout": 2, "stat": 3}
	ordered := make([]*Entry, len(entries))
	for i := range entries {
		ordered[i] = &entries[i]
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftRank, leftKnown := classRank[ordered[i].Class]
		rightRank, rightKnown := classRank[ordered[j].Class]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && leftRank != rightRank {
			return leftRank < rightRank
		}
		if ordered[i].Class != ordered[j].Class {
			return ordered[i].Class < ordered[j].Class
		}
		return ordered[i].Key < ordered[j].Key
	})

	var output strings.Builder
	previousClass := ""
	for _, entry := range ordered {
		if previousClass != entry.Class {
			if output.Len() > 0 {
				output.WriteString("\n\n")
			}
			output.WriteString(terminal.Heading(strings.ToUpper(entry.Class), mode))
			output.WriteString("\n\n")
			previousClass = entry.Class
		} else {
			output.WriteString("\n\n")
		}
		output.WriteString(RenderEntryWithMode(entry, mode))
	}
	return output.String()
}

// SelectMatch resolves one one-based selection from an ambiguous match set.
func SelectMatch(entries []*Entry, choice string) *Entry {
	number, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil || number < 1 || number > len(entries) {
		return nil
	}
	return entries[number-1]
}
