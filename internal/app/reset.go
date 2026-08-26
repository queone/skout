package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

const (
	resetAbsentMessage  = "No database found — nothing to reset.\n"
	resetCancelled      = "Cancelled.\n"
	resetSuccessMessage = "Database deleted. Run skout sync to rebuild.\n"
)

type resetGuard interface {
	Close() error
}

type resetOptions struct {
	DatabasePath string
	Input        io.Reader
	Output       io.Writer
	Mode         terminal.ColorMode
	Acquire      func(string, store.DatabaseGuardMode) (resetGuard, error)
	Lstat        func(string) (fs.FileInfo, error)
	Remove       func(string) error
}

// ResetProduction performs the confirmed reset against the production database path.
func ResetProduction(input io.Reader, output io.Writer, mode terminal.ColorMode) error {
	path, err := store.DatabasePath()
	if err != nil {
		return fmt.Errorf("reset: resolve database: %w", err)
	}
	return resetWith(resetOptions{DatabasePath: path, Input: input, Output: output, Mode: mode})
}

func resetWith(options resetOptions) error {
	if strings.TrimSpace(options.DatabasePath) == "" {
		return fmt.Errorf("reset: resolve database: path is empty; correct the path and retry")
	}
	if options.Input == nil {
		options.Input = strings.NewReader("")
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Acquire == nil {
		options.Acquire = func(path string, mode store.DatabaseGuardMode) (resetGuard, error) {
			return store.AcquireDatabaseGuard(path, mode)
		}
	}
	if options.Lstat == nil {
		options.Lstat = os.Lstat
	}
	if options.Remove == nil {
		options.Remove = os.Remove
	}
	paths := resetDatabaseFamily(options.DatabasePath)

	initial, err := inspectResetFamilyWithGuard(options, paths, "preflight")
	if err != nil {
		return err
	}
	if len(initial) == 0 {
		return writeResetResult(options.Output, resetAbsentMessage, "absent result", false)
	}
	prompt := fmt.Sprintf("This will delete %s and require a full re-sync.\nContinue? [y/N] ", resetSection(options.DatabasePath, options.Mode))
	if err := writeResetOutput(options.Output, prompt); err != nil {
		return fmt.Errorf("reset: write confirmation: %w; database was not deleted", err)
	}
	answer, err := bufio.NewReader(options.Input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reset: read confirmation: %w; database was not deleted", err)
	}
	if answer = strings.TrimSpace(strings.ToLower(answer)); answer != "y" && answer != "yes" {
		return writeResetResult(options.Output, resetCancelled, "cancellation result", false)
	}

	guard, err := options.Acquire(options.DatabasePath, store.DatabaseGuardExclusive)
	if err != nil {
		return fmt.Errorf("reset: acquire exclusive database operation guard: %w", err)
	}
	current, inspectErr := inspectResetFamily(options, paths)
	if inspectErr != nil {
		return errors.Join(inspectErr, closeResetGuard(guard, "after revalidation", false))
	}
	if len(current) == 0 {
		if err := closeResetGuard(guard, "after vanished-family revalidation", false); err != nil {
			return err
		}
		return writeResetResult(options.Output, resetAbsentMessage, "absent result", false)
	}

	removed := make([]string, 0, len(current))
	for _, path := range paths {
		if !current[path] {
			continue
		}
		info, err := options.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return resetDeletionFailure(options, guard, paths, removed, fmt.Errorf("inspect %s before deletion: %w", path, err))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return resetDeletionFailure(options, guard, paths, removed, fmt.Errorf("inspect %s before deletion: target is not a regular non-symlink file", path))
		}
		if err := options.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return resetDeletionFailure(options, guard, paths, removed, fmt.Errorf("remove %s: %w", path, err))
		}
		removed = append(removed, path)
	}
	remaining, err := inspectResetFamily(options, paths)
	if err != nil {
		return resetDeletionFailure(options, guard, paths, removed, fmt.Errorf("verify deletion: %w", err))
	}
	if len(remaining) != 0 {
		return resetDeletionFailure(options, guard, paths, removed, errors.New("one or more database-family targets remained after deletion"))
	}
	if err := closeResetGuard(guard, "after deletion", true); err != nil {
		return err
	}
	return writeResetResult(options.Output, resetSuccess(options.Mode), "success result", true)
}

func inspectResetFamilyWithGuard(options resetOptions, paths []string, phase string) (map[string]bool, error) {
	guard, err := options.Acquire(options.DatabasePath, store.DatabaseGuardExclusive)
	if err != nil {
		return nil, fmt.Errorf("reset: acquire exclusive database operation guard during %s: %w", phase, err)
	}
	present, inspectErr := inspectResetFamily(options, paths)
	closeErr := closeResetGuard(guard, "after "+phase, false)
	return present, errors.Join(inspectErr, closeErr)
}

func inspectResetFamily(options resetOptions, paths []string) (map[string]bool, error) {
	present := make(map[string]bool, len(paths))
	for _, path := range paths {
		info, err := options.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reset: inspect database family %s: %w; correct the path and retry", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("reset: inspect database family %s: target is not a regular non-symlink file; correct the path and retry", path)
		}
		present[path] = true
	}
	return present, nil
}

func resetDeletionFailure(options resetOptions, guard resetGuard, paths, removed []string, cause error) error {
	remaining, inspectErr := inspectResetFamily(options, paths)
	remainingPaths := orderedPresent(paths, remaining)
	detail := fmt.Errorf("reset: delete database family: %w; reset is incomplete (removed: %s; remaining: %s); correct the condition and run skout reset again", cause, resetPathList(removed), resetPathList(remainingPaths))
	if inspectErr != nil {
		detail = fmt.Errorf("reset: delete database family: %w; reset may be incomplete (removed: %s; remaining state unavailable: %v); inspect the database family and run skout reset again", cause, resetPathList(removed), inspectErr)
	}
	return errors.Join(detail, closeResetGuard(guard, "after incomplete deletion", len(removed) > 0))
}

func closeResetGuard(guard resetGuard, phase string, mutated bool) error {
	if guard == nil {
		return nil
	}
	if err := guard.Close(); err != nil {
		if mutated {
			return fmt.Errorf("reset: release database operation guard %s: %w; database deletion completed or may be partial, so inspect local state before retry", phase, err)
		}
		return fmt.Errorf("reset: release database operation guard %s: %w; database was not deleted", phase, err)
	}
	return nil
}

func writeResetResult(output io.Writer, value, operation string, mutated bool) error {
	if err := writeResetOutput(output, value); err != nil {
		if mutated {
			return fmt.Errorf("reset: write %s: %w; database deletion completed, so inspect local state before retry", operation, err)
		}
		return fmt.Errorf("reset: write %s: %w; database was not deleted", operation, err)
	}
	return nil
}

func writeResetOutput(output io.Writer, value string) error {
	written, err := io.WriteString(output, value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func resetDatabaseFamily(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func orderedPresent(paths []string, present map[string]bool) []string {
	result := make([]string, 0, len(present))
	for _, path := range paths {
		if present[path] {
			result = append(result, path)
		}
	}
	return result
}

func resetPathList(paths []string) string {
	if len(paths) == 0 {
		return "none"
	}
	return strings.Join(paths, ", ")
}

func resetSection(value string, mode terminal.ColorMode) string {
	if mode != terminal.Color {
		return value
	}
	return "\x1b[38;5;255m" + value + "\x1b[0m"
}

func resetSuccess(mode terminal.ColorMode) string {
	if mode != terminal.Color {
		return resetSuccessMessage
	}
	return "Database deleted. Run \x1b[38;5;46mskout sync\x1b[0m to rebuild.\n"
}
