package terminal

import (
	"strings"
	"testing"
)

func TestSelectColorModeUsesTerminalAndEnvironmentEvidence(t *testing.T) {
	tests := []struct {
		name    string
		context ColorContext
		want    ColorMode
	}{
		{name: "NonTerminal", context: ColorContext{Term: "xterm-256color"}, want: Plain},
		{name: "NoColor", context: ColorContext{StdoutIsTerminal: true, NoColor: "1", Term: "xterm-256color"}, want: Plain},
		{name: "DumbTerminal", context: ColorContext{StdoutIsTerminal: true, Term: "dumb", ColorTerm: "truecolor"}, want: Plain},
		{name: "UnsupportedTerminal", context: ColorContext{StdoutIsTerminal: true, Term: "xterm"}, want: Plain},
		{name: "TrueColor", context: ColorContext{StdoutIsTerminal: true, ColorTerm: "truecolor"}, want: Color},
		{name: "TwentyFourBit", context: ColorContext{StdoutIsTerminal: true, ColorTerm: "24bit"}, want: Color},
		{name: "TwoHundredFiftySixColor", context: ColorContext{StdoutIsTerminal: true, Term: "screen-256color"}, want: Color},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SelectColorMode(test.context); got != test.want {
				t.Fatalf("SelectColorMode() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStyleRolesAreExactAndPlainModeHasNoANSI(t *testing.T) {
	if got, want := Title("skout", Color), "\x1b[1;38;5;231mskout\x1b[0m"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if got, want := Subtitle("advisor", Color), "\x1b[38;5;245madvisor\x1b[0m"; got != want {
		t.Errorf("Subtitle() = %q, want %q", got, want)
	}
	if got, want := Section("USAGE", Color), "\x1b[38;5;255mUSAGE\x1b[0m"; got != want {
		t.Errorf("Section() = %q, want %q", got, want)
	}
	if got, want := Usage("Usage:", Color), "\x1b[1;38;5;231mUsage:\x1b[0m"; got != want {
		t.Errorf("Usage() = %q, want %q", got, want)
	}
	if got, want := Heading("BASEBALL", Color), "\x1b[38;5;33mBASEBALL\x1b[0m"; got != want {
		t.Errorf("Heading() = %q, want %q", got, want)
	}
	if got, want := Alias("Aliases: PA", Color), "\x1b[38;5;245mAliases: PA\x1b[0m"; got != want {
		t.Errorf("Alias() = %q, want %q", got, want)
	}
	for name, got := range map[string]string{
		"TableHeading": TableHeading("TEAM", Color),
		"Dim":          Dim("context", Color),
		"Good":         Good("available", Color),
		"Warning":      Warning("warning", Color),
		"Injury":       Injury("IL", Color),
		"RosterRow":    RosterRow("row", "D10", Color),
	} {
		if !strings.HasPrefix(got, "\x1b[") || !strings.HasSuffix(got, "\x1b[0m") {
			t.Errorf("%s color output=%q", name, got)
		}
		if VisibleWidth(got) == 0 {
			t.Errorf("%s visible width is zero", name)
		}
	}
	if got := RosterRow("row", "A", Color); got != "row" {
		t.Errorf("active row=%q", got)
	}

	for name, test := range map[string]struct{ got, want string }{
		"Title":    {Title("skout", Plain), "skout"},
		"Subtitle": {Subtitle("advisor", Plain), "advisor"},
		"Section":  {Section("USAGE", Plain), "USAGE"},
		"Usage":    {Usage("Usage:", Plain), "Usage:"},
		"Heading":  {Heading("BASEBALL", Plain), "BASEBALL"},
		"Alias":    {Alias("Aliases: PA", Plain), "Aliases: PA"},
	} {
		if test.got != test.want || strings.Contains(test.got, "\x1b[") {
			t.Errorf("%s plain output = %q, want %q", name, test.got, test.want)
		}
	}
}

func TestLineupIndicatorImplementsAllColorStates(t *testing.T) {
	tests := []struct {
		name               string
		favorable, subdued bool
		want               string
	}{
		{name: "Favorable", favorable: true, want: "\x1b[38;5;46m●\x1b[0m"},
		{name: "Unfavorable", want: "\x1b[38;5;196m●\x1b[0m"},
		{name: "SubduedFavorable", favorable: true, subdued: true, want: "\x1b[38;5;34m●\x1b[38;5;245m"},
		{name: "SubduedUnfavorable", subdued: true, want: "\x1b[38;5;124m●\x1b[38;5;245m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := LineupIndicator("●", test.favorable, test.subdued, Color); got != test.want {
				t.Errorf("LineupIndicator() = %q, want %q", got, test.want)
			}
			if got := LineupIndicator("●", test.favorable, test.subdued, Plain); got != "●" {
				t.Errorf("plain LineupIndicator() = %q", got)
			}
		})
	}
}
