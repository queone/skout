// Package terminal provides terminal-aware styling for the implemented CLI surface.
package terminal

import (
	"fmt"
	"os"
	"strings"
)

// ColorMode selects plain or ANSI-colored output.
type ColorMode uint8

const (
	// Plain emits text without ANSI escape sequences.
	Plain ColorMode = iota
	// Color emits the governed 256-color roles.
	Color
)

// ColorContext contains the terminal and environment evidence used for color selection.
type ColorContext struct {
	StdoutIsTerminal bool
	NoColor          string
	Term             string
	ColorTerm        string
}

// SelectColorMode resolves the output mode from terminal and environment evidence.
func SelectColorMode(context ColorContext) ColorMode {
	disabled := !context.StdoutIsTerminal || context.NoColor != "" || context.Term == "dumb"
	advertised := context.ColorTerm == "truecolor" || context.ColorTerm == "24bit" || strings.Contains(context.Term, "256color")
	if !disabled && advertised {
		return Color
	}
	return Plain
}

// IsTerminal reports whether file is attached to a character device.
func IsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Usage styles a help usage label with the governed bold-white role.
func Usage(value string, mode ColorMode) string {
	return style(value, "1;38;5;231", mode)
}

// Heading styles glossary class and entry headings with the governed blue role.
func Heading(value string, mode ColorMode) string {
	return style(value, "38;5;33", mode)
}

// Alias styles glossary alias lines with the governed gray role.
func Alias(value string, mode ColorMode) string {
	return style(value, "38;5;245", mode)
}

func style(value, role string, mode ColorMode) string {
	if mode != Color {
		return value
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", role, value)
}
