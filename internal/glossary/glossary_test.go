package glossary

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/queone/skout/internal/terminal"
)

const (
	expectedKeySetSHA256     = "18b014b5c062db5409cbc2b223706c99a7739dab0d0b78129e1154bdd85a1a9f"
	expectedChecklistSHA256  = "de7f567663f919ae37d146067a3d002d5f3ea4de992bda81af6629782efb8d24"
	expectedFullOutputSHA256 = "d1ca06f25a22f4bf40d1d0c23e3f04d9b5404502dc620e109bc26799af9b4f08"
)

func TestEmbeddedGlossaryPreservesFrozenSemanticData(t *testing.T) {
	entries, err := EmbeddedEntries()
	if err != nil {
		t.Fatalf("EmbeddedEntries() error = %v", err)
	}
	if got, want := len(entries), expectedEntryCount; got != want {
		t.Fatalf("entry count = %d, want %d", got, want)
	}

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	sort.Strings(keys)
	keyDigest := sha256.Sum256([]byte(strings.Join(keys, "\n") + "\n"))
	if got := hex.EncodeToString(keyDigest[:]); got != expectedKeySetSHA256 {
		t.Errorf("sorted key-set SHA-256 = %s, want %s", got, expectedKeySetSHA256)
	}

	pa := findEntry(t, entries, "pa")
	if got, want := pa.Term, "Plate Appearance"; got != want {
		t.Errorf("pa term = %q, want %q", got, want)
	}
	if got, want := strings.Join(pa.Aliases, ","), "PA"; got != want {
		t.Errorf("pa aliases = %q, want %q", got, want)
	}
	if !strings.Contains(pa.Definition, "Any completed turn at bat — includes") {
		t.Errorf("pa definition lost frozen content: %q", pa.Definition)
	}
	if got := findEntry(t, entries, "game-log").Class; got != "skout" {
		t.Errorf("game-log class = %q, want skout", got)
	}
}

func TestEmbeddedGlossaryOwnsGoPreambleAndOmitsRustLocations(t *testing.T) {
	wantPreamble := "Canonical definitions for baseball, fantasy, and skout-specific terms. This file is embedded in the Go binary and powers skout i."
	if !strings.Contains(embeddedSource, wantPreamble) {
		t.Errorf("embedded glossary is missing Go-owned preamble")
	}
	for _, stale := range []string{"**Where:**", "Rust binary", "src/", "statcast_seasons.fastball_velo"} {
		if strings.Contains(embeddedSource, stale) {
			t.Errorf("embedded glossary contains stale location %q", stale)
		}
	}

	var checklist string
	lines := strings.Split(embeddedSource, "\n")
	for index, line := range lines {
		if line != "## Coverage Checklist" {
			continue
		}
		for _, candidate := range lines[index+1:] {
			if strings.HasPrefix(candidate, "`") {
				checklist = candidate
				break
			}
		}
		break
	}
	if checklist == "" {
		t.Fatal("coverage checklist line is missing")
	}
	digest := sha256.Sum256([]byte(checklist))
	if got := hex.EncodeToString(digest[:]); got != expectedChecklistSHA256 {
		t.Errorf("coverage checklist SHA-256 = %s, want %s", got, expectedChecklistSHA256)
	}
}

func TestParserRejectsMalformedOrIncompleteEntries(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		message string
	}{
		{name: "MalformedHeading", source: "### Broken heading\nDefinition.\n", message: "glossary line 1: malformed entry heading"},
		{name: "EmptyTerm", source: "###  (`key`) [class]\nDefinition.\n", message: "entry term, key, and class must be non-empty"},
		{name: "EmptyKey", source: "### Term (``) [class]\nDefinition.\n", message: "entry term, key, and class must be non-empty"},
		{name: "EmptyClass", source: "### Term (`key`) []\nDefinition.\n", message: "entry term, key, and class must be non-empty"},
		{name: "DuplicateKey", source: "### One (`key`) [class]\nOne.\n### Two (`key`) [class]\nTwo.\n", message: "glossary line 3: duplicate entry key \"key\""},
		{name: "EmptyDefinition", source: "### Term (`key`) [class]\n\n- **Aliases:** T\n", message: "glossary line 1: entry \"key\" has an empty definition"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parse(test.source)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("parse() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestEmbeddedValidationRejectsWrongEntryCount(t *testing.T) {
	_, err := parseEmbedded("### Term (`key`) [class]\n\nDefinition.\n")
	if err == nil || !strings.Contains(err.Error(), "expected 113 baseline entries, found 1") {
		t.Fatalf("parseEmbedded() error = %v, want count diagnostic", err)
	}
}

func TestLookupUsesExactPrecedenceAndSourceOrderedSubstrings(t *testing.T) {
	entries := []Entry{
		testEntry("alpha", "Shared", "first"),
		testEntry("shared", "Second", "alias"),
		testEntry("third", "Alias", "shared"),
	}
	if got := Lookup(entries, "shared").Entry; got == nil || got.Key != "shared" {
		t.Fatalf("exact key match = %#v, want shared", got)
	}
	if got := Lookup(entries, "second").Entry; got == nil || got.Key != "shared" {
		t.Fatalf("exact term match = %#v, want shared", got)
	}
	if got := Lookup(entries, "alias").Entry; got == nil || got.Key != "third" {
		t.Fatalf("exact alias match = %#v, want third", got)
	}
	result := Lookup(entries, "s")
	if got, want := matchKeys(result.Matches), "alpha,shared,third"; got != want {
		t.Errorf("substring matches = %q, want %q", got, want)
	}
	if got := Lookup(entries, "ALPHA").Entry; got == nil || got.Key != "alpha" {
		t.Fatalf("case-insensitive match = %#v, want alpha", got)
	}
}

func TestSuggestionsUseUnicodeCodePointsAndLexicographicTies(t *testing.T) {
	entries := []Entry{
		testEntry("café", "Cafe"),
		testEntry("caff", "Caff"),
		testEntry("case", "Case"),
		testEntry("zzzz", "Zed"),
	}
	if got, want := strings.Join(SuggestKeys(entries, "cafe", 3), ","), "caff,café,case"; got != want {
		t.Errorf("suggestions = %q, want %q", got, want)
	}
	if got, want := strings.Join(Lookup(entries, "cafg").Suggestions, ","), "caff,café,case"; got != want {
		t.Errorf("miss suggestions = %q, want %q", got, want)
	}
}

func TestRenderingPreservesOrderingRolesAndOutputDigest(t *testing.T) {
	entry := testEntry("b", "Beta", "B")
	if got, want := RenderEntry(&entry), "Beta (b) [test]\nAliases: B\nDefinition for b."; got != want {
		t.Errorf("RenderEntry() = %q, want %q", got, want)
	}
	colored := RenderEntryWithMode(&entry, terminal.Color)
	if !strings.HasPrefix(colored, "\x1b[38;5;33mBeta (b) [test]\x1b[0m") {
		t.Errorf("colored heading has wrong role: %q", colored)
	}
	if !strings.Contains(colored, "\x1b[38;5;245mAliases: B\x1b[0m") {
		t.Errorf("colored aliases have wrong role: %q", colored)
	}

	entries, err := EmbeddedEntries()
	if err != nil {
		t.Fatalf("EmbeddedEntries() error = %v", err)
	}
	plain := RenderFull(entries)
	if strings.Contains(plain, "\x1b[") {
		t.Error("plain full rendering contains ANSI")
	}
	if strings.HasSuffix(plain, "\n\n") {
		t.Error("plain full rendering has a trailing blank line")
	}
	baseball := strings.Index(plain, "BASEBALL\n")
	fantasy := strings.Index(plain, "FANTASY\n")
	skout := strings.Index(plain, "SKOUT\n")
	stat := strings.Index(plain, "STAT\n")
	if !(baseball >= 0 && baseball < fantasy && fantasy < skout && skout < stat) {
		t.Errorf("class order is wrong: baseball=%d fantasy=%d skout=%d stat=%d", baseball, fantasy, skout, stat)
	}
	digest := sha256.Sum256([]byte(plain + "\n"))
	if got := hex.EncodeToString(digest[:]); got != expectedFullOutputSHA256 {
		t.Errorf("full plain-output SHA-256 = %s, want %s", got, expectedFullOutputSHA256)
	}
}

func TestSelectMatchUsesTrimmedOneBasedInput(t *testing.T) {
	entries := []Entry{testEntry("alpha", "Alpha"), testEntry("beta", "Beta")}
	matches := []*Entry{&entries[0], &entries[1]}
	if got := SelectMatch(matches, " 2 "); got == nil || got.Key != "beta" {
		t.Fatalf("SelectMatch() = %#v, want beta", got)
	}
	for _, invalid := range []string{"0", "3", "not-a-number"} {
		if got := SelectMatch(matches, invalid); got != nil {
			t.Errorf("SelectMatch(%q) = %#v, want nil", invalid, got)
		}
	}
}

func findEntry(t *testing.T, entries []Entry, key string) *Entry {
	t.Helper()
	for index := range entries {
		if entries[index].Key == key {
			return &entries[index]
		}
	}
	t.Fatalf("embedded glossary is missing key %q", key)
	return nil
}

func testEntry(key, term string, aliases ...string) Entry {
	return Entry{
		Key:        key,
		Term:       term,
		Class:      "test",
		Aliases:    aliases,
		Definition: "Definition for " + key + ".",
	}
}

func matchKeys(entries []*Entry) string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return strings.Join(keys, ",")
}
