package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

func TestResetAbsentCancellationAndAffirmativeContracts(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		var output resetFlushBuffer
		if err := resetWith(resetOptions{DatabasePath: path, Input: errorReader{errors.New("input must not be read")}, Output: &output}); err != nil {
			t.Fatal(err)
		}
		if output.String() != resetAbsentMessage || output.flushes != 1 {
			t.Fatalf("output=%q flushes=%d", output.String(), output.flushes)
		}
	})

	for _, answer := range []string{"", "\n", "n\n", "no\n", "true\n", " y e s \n"} {
		t.Run("cancel "+strings.TrimSpace(answer), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skout.db")
			createResetFamily(t, path)
			var output resetFlushBuffer
			if err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader(answer), Output: &output}); err != nil {
				t.Fatal(err)
			}
			want := resetPrompt(path) + resetCancelled
			if output.String() != want || output.flushes != 2 {
				t.Fatalf("output=%q flushes=%d want=%q", output.String(), output.flushes, want)
			}
			assertResetPaths(t, resetDatabaseFamily(path), true)
		})
	}

	for _, answer := range []string{"y\n", "yes\n", " Y \n", " YeS \n", "yes"} {
		t.Run("confirm "+strings.TrimSpace(answer), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "skout.db")
			createResetFamily(t, path)
			preserved := []string{
				filepath.Join(root, "config.json"),
				filepath.Join(root, "skout.db-extra"),
				filepath.Join(root, "cache-entry"),
			}
			for _, target := range preserved {
				if err := os.WriteFile(target, []byte("keep"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			var output resetFlushBuffer
			source := strings.NewReader(answer)
			input := readerFunc(func(buffer []byte) (int, error) {
				if output.flushes != 1 {
					t.Fatalf("prompt flushes before read=%d", output.flushes)
				}
				return source.Read(buffer)
			})
			if err := resetWith(resetOptions{DatabasePath: path, Input: input, Output: &output}); err != nil {
				t.Fatal(err)
			}
			want := resetPrompt(path) + resetSuccessMessage
			if output.String() != want || output.flushes != 2 {
				t.Fatalf("output=%q flushes=%d want=%q", output.String(), output.flushes, want)
			}
			assertResetPaths(t, resetDatabaseFamily(path), false)
			for _, target := range preserved {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "keep" {
					t.Errorf("preserved %s data=%q err=%v", target, data, err)
				}
			}
		})
	}
}

func TestResetSidecarOnlyVanishedAndColorResults(t *testing.T) {
	t.Run("sidecar only", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path+"-wal", []byte("wal"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output}); err != nil {
			t.Fatal(err)
		}
		if output.String() != resetPrompt(path)+resetSuccessMessage {
			t.Fatalf("output=%q", output.String())
		}
		assertResetPaths(t, resetDatabaseFamily(path), false)
	})

	t.Run("vanished after confirmation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		input := readerFunc(func(buffer []byte) (int, error) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			return copy(buffer, "yes\n"), nil
		})
		if err := resetWith(resetOptions{DatabasePath: path, Input: input, Output: &output}); err != nil {
			t.Fatal(err)
		}
		if output.String() != resetPrompt(path)+resetAbsentMessage {
			t.Fatalf("output=%q", output.String())
		}
	})

	t.Run("color", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("y\n"), Output: &output, Mode: terminal.Color}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "\x1b[38;5;255m"+path+"\x1b[0m") || !strings.Contains(output.String(), "\x1b[38;5;46mskout sync\x1b[0m") {
			t.Fatalf("color output=%q", output.String())
		}
	})
}

func TestResetRejectsUnsafeTargetsBeforePrompt(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"directory": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
		"named pipe": func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skout.db")
			setup(t, path)
			var output bytes.Buffer
			err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output})
			if err == nil || !strings.Contains(err.Error(), "not a regular non-symlink file") || output.Len() != 0 {
				t.Fatalf("err=%v output=%q", err, output.String())
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("unsafe target changed: %v", err)
			}
			if name == "symlink" {
				data, err := os.ReadFile(path + ".target")
				if err != nil || string(data) != "keep" {
					t.Fatalf("symlink target data=%q err=%v", data, err)
				}
			}
		})
	}

	t.Run("target changes after prompt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		target := path + ".target"
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		input := readerFunc(func(buffer []byte) (int, error) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return copy(buffer, "yes\n"), nil
		})
		err := resetWith(resetOptions{DatabasePath: path, Input: input, Output: &output})
		if err == nil || !strings.Contains(err.Error(), "not a regular non-symlink file") || output.String() != resetPrompt(path) {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "keep" {
			t.Fatalf("target data=%q err=%v", data, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("replacement symlink info=%v err=%v", info, err)
		}
	})
}

func TestResetRefusesSharedDatabaseOwnersBeforeAndAfterPrompt(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		shared, err := store.AcquireDatabaseGuard(path, store.DatabaseGuardShared)
		if err != nil {
			t.Fatal(err)
		}
		defer shared.Close()
		var output bytes.Buffer
		err = resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output})
		if err == nil || !strings.Contains(err.Error(), "another skout command") || output.Len() != 0 {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
		assertResetPaths(t, []string{path}, true)
	})

	t.Run("after prompt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		acquire := func(string, store.DatabaseGuardMode) (resetGuard, error) {
			calls++
			if calls == 1 {
				return &fakeResetGuard{}, nil
			}
			return nil, errors.New("another skout command is using the local database")
		}
		var output bytes.Buffer
		err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output, Acquire: acquire})
		if err == nil || !strings.Contains(err.Error(), "another skout command") || output.String() != resetPrompt(path) {
			t.Fatalf("calls=%d err=%v output=%q", calls, err, output.String())
		}
		assertResetPaths(t, []string{path}, true)
	})
}

func TestResetDeletionOrderAndPartialRecovery(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		family := resetDatabaseFamily(path)
		createResetFamily(t, path)
		var removed []string
		var output bytes.Buffer
		err := resetWith(resetOptions{
			DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output,
			Remove: func(target string) error {
				removed = append(removed, target)
				return os.Remove(target)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(removed, family) || output.String() != resetPrompt(path)+resetSuccessMessage {
			t.Fatalf("removed=%v output=%q", removed, output.String())
		}
	})

	for _, failure := range []string{"primary", "wal"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skout.db")
			family := resetDatabaseFamily(path)
			createResetFamily(t, path)
			failPath := path
			if failure == "wal" {
				failPath = path + "-wal"
			}
			var removed []string
			remove := func(target string) error {
				removed = append(removed, target)
				if target == failPath {
					return errors.New("injected deletion failure")
				}
				return os.Remove(target)
			}
			var output bytes.Buffer
			err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output, Remove: remove})
			if err == nil || !strings.Contains(err.Error(), "run skout reset again") || strings.Contains(output.String(), resetSuccessMessage) {
				t.Fatalf("err=%v output=%q", err, output.String())
			}
			if failure == "primary" {
				if !slices.Equal(removed, []string{path}) || !strings.Contains(err.Error(), "removed: none") {
					t.Fatalf("removed=%v err=%v", removed, err)
				}
				assertResetPaths(t, family, true)
			} else {
				if !slices.Equal(removed, []string{path, path + "-wal"}) || !strings.Contains(err.Error(), "removed: "+path) {
					t.Fatalf("removed=%v err=%v", removed, err)
				}
				assertResetPaths(t, []string{path}, false)
				assertResetPaths(t, family[1:], true)
			}
		})
	}
}

func TestResetPropagatesInspectionInputOutputAndGuardFailures(t *testing.T) {
	t.Run("inspection", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		lstat := func(target string) (os.FileInfo, error) {
			if target == path+"-wal" {
				return nil, errors.New("injected inspection failure")
			}
			return os.Lstat(target)
		}
		var output bytes.Buffer
		err := resetWith(resetOptions{DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output, Lstat: lstat})
		if err == nil || !strings.Contains(err.Error(), "injected inspection failure") || output.Len() != 0 {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
		assertResetPaths(t, []string{path}, true)
	})

	for _, test := range []struct {
		name           string
		answer         io.Reader
		writer         *resetFailureWriter
		want           string
		deleted        bool
		createDatabase bool
	}{
		{name: "absent result write", answer: strings.NewReader(""), writer: &resetFailureWriter{failWriteAt: 1}, want: "database was not deleted"},
		{name: "prompt write", answer: strings.NewReader("yes\n"), writer: &resetFailureWriter{failWriteAt: 1}, want: "database was not deleted", createDatabase: true},
		{name: "prompt flush", answer: strings.NewReader("yes\n"), writer: &resetFailureWriter{failFlushAt: 1}, want: "database was not deleted", createDatabase: true},
		{name: "confirmation read", answer: errorReader{errors.New("injected read failure")}, writer: &resetFailureWriter{}, want: "database was not deleted", createDatabase: true},
		{name: "cancellation result write", answer: strings.NewReader("no\n"), writer: &resetFailureWriter{failWriteAt: 2}, want: "database was not deleted", createDatabase: true},
		{name: "success result write", answer: strings.NewReader("yes\n"), writer: &resetFailureWriter{failWriteAt: 2}, want: "deletion completed", deleted: true, createDatabase: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skout.db")
			if test.createDatabase {
				if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := resetWith(resetOptions{DatabasePath: path, Input: test.answer, Output: test.writer})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v writes=%d flushes=%d", err, test.writer.writes, test.writer.flushes)
			}
			_, statErr := os.Stat(path)
			if test.deleted && !os.IsNotExist(statErr) {
				t.Fatalf("database remained: %v", statErr)
			}
			if !test.deleted && test.createDatabase && statErr != nil {
				t.Fatalf("database changed: %v", statErr)
			}
		})
	}

	t.Run("guard release before mutation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err := resetWith(resetOptions{
			DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output,
			Acquire: func(string, store.DatabaseGuardMode) (resetGuard, error) {
				return &fakeResetGuard{err: errors.New("injected close failure")}, nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "database was not deleted") || output.Len() != 0 {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
		assertResetPaths(t, []string{path}, true)
	})

	t.Run("guard release after mutation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "skout.db")
		if err := os.WriteFile(path, []byte("database"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		var output bytes.Buffer
		err := resetWith(resetOptions{
			DatabasePath: path, Input: strings.NewReader("yes\n"), Output: &output,
			Acquire: func(string, store.DatabaseGuardMode) (resetGuard, error) {
				calls++
				if calls == 2 {
					return &fakeResetGuard{err: errors.New("injected close failure")}, nil
				}
				return &fakeResetGuard{}, nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "completed or may be partial") || output.String() != resetPrompt(path) {
			t.Fatalf("err=%v output=%q", err, output.String())
		}
		assertResetPaths(t, []string{path}, false)
	})
}

func TestResetProductionReportsPathResolutionFailure(t *testing.T) {
	t.Setenv("HOME", "")
	var output bytes.Buffer
	err := ResetProduction(strings.NewReader("yes\n"), &output, terminal.Plain)
	if err == nil || !strings.Contains(err.Error(), "reset: resolve database") || output.Len() != 0 {
		t.Fatalf("err=%v output=%q", err, output.String())
	}
}

func createResetFamily(t *testing.T, path string) {
	t.Helper()
	for _, target := range resetDatabaseFamily(path) {
		if err := os.WriteFile(target, []byte(filepath.Base(target)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertResetPaths(t *testing.T, paths []string, present bool) {
	t.Helper()
	for _, path := range paths {
		_, err := os.Lstat(path)
		if present && err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
		if !present && !os.IsNotExist(err) {
			t.Errorf("expected %s absent: %v", path, err)
		}
	}
}

func resetPrompt(path string) string {
	return "This will delete " + path + " and require a full re-sync.\nContinue? [y/N] "
}

type resetFlushBuffer struct {
	bytes.Buffer
	flushes int
}

func (buffer *resetFlushBuffer) Flush() error {
	buffer.flushes++
	return nil
}

type resetFailureWriter struct {
	bytes.Buffer
	writes, flushes          int
	failWriteAt, failFlushAt int
}

func (writer *resetFailureWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failWriteAt {
		return 0, errors.New("injected output failure")
	}
	return writer.Buffer.Write(value)
}

func (writer *resetFailureWriter) WriteString(value string) (int, error) {
	return writer.Write([]byte(value))
}

func (writer *resetFailureWriter) Flush() error {
	writer.flushes++
	if writer.flushes == writer.failFlushAt {
		return errors.New("injected flush failure")
	}
	return nil
}

type readerFunc func([]byte) (int, error)

func (reader readerFunc) Read(buffer []byte) (int, error) { return reader(buffer) }

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type fakeResetGuard struct{ err error }

func (guard *fakeResetGuard) Close() error { return guard.err }
