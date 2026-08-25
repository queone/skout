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
	if got, want := Usage("Usage:", Color), "\x1b[1;38;5;231mUsage:\x1b[0m"; got != want {
		t.Errorf("Usage() = %q, want %q", got, want)
	}
	if got, want := Heading("BASEBALL", Color), "\x1b[38;5;33mBASEBALL\x1b[0m"; got != want {
		t.Errorf("Heading() = %q, want %q", got, want)
	}
	if got, want := Alias("Aliases: PA", Color), "\x1b[38;5;245mAliases: PA\x1b[0m"; got != want {
		t.Errorf("Alias() = %q, want %q", got, want)
	}

	for name, output := range map[string]string{
		"Usage":   Usage("Usage:", Plain),
		"Heading": Heading("BASEBALL", Plain),
		"Alias":   Alias("Aliases: PA", Plain),
	} {
		if strings.Contains(output, "\x1b[") {
			t.Errorf("%s plain output contains ANSI: %q", name, output)
		}
	}
}
