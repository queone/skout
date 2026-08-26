package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// DatabaseGuardMode selects shared command access or exclusive reset access.
type DatabaseGuardMode uint8

const (
	DatabaseGuardShared DatabaseGuardMode = iota + 1
	DatabaseGuardExclusive
)

// DatabaseGuard owns one process-level local-database operation lock.
type DatabaseGuard struct {
	file *os.File
	path string
}

// AcquireDatabaseGuard acquires a non-blocking operation lock without opening SQLite.
func AcquireDatabaseGuard(databasePath string, mode DatabaseGuardMode) (*DatabaseGuard, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, fmt.Errorf("database operation guard: database path is empty; correct the path and retry")
	}
	if mode != DatabaseGuardShared && mode != DatabaseGuardExclusive {
		return nil, fmt.Errorf("database operation guard: mode is invalid; correct the caller and retry")
	}
	runtimeDirectory := filepath.Join(filepath.Dir(databasePath), "runtime")
	if err := prepareGuardDirectory(runtimeDirectory); err != nil {
		return nil, err
	}
	path := filepath.Join(runtimeDirectory, "database.lock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, guardPathError("inspect lock", path, errors.New("target is not a regular non-symlink file"))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, guardPathError("inspect lock", path, err)
	}
	descriptor, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, guardPathError("open lock", path, err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	cleanup := func() {
		_ = file.Close()
	}
	info, err := file.Stat()
	if err != nil {
		cleanup()
		return nil, guardPathError("inspect opened lock", path, err)
	}
	if !info.Mode().IsRegular() {
		cleanup()
		return nil, guardPathError("inspect opened lock", path, errors.New("target is not a regular file"))
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return nil, guardPathError("secure lock", path, err)
	}
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if mode == DatabaseGuardExclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err := syscall.Flock(descriptor, operation); err != nil {
		cleanup()
		return nil, fmt.Errorf("database operation guard: another skout command is using the local database; wait for it to finish and retry")
	}
	return &DatabaseGuard{file: file, path: path}, nil
}

// Close releases the operation lock and is safe to repeat.
func (guard *DatabaseGuard) Close() error {
	if guard == nil || guard.file == nil {
		return nil
	}
	file := guard.file
	guard.file = nil
	var failures []error
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		failures = append(failures, guardPathError("release lock", guard.path, err))
	}
	if err := file.Close(); err != nil {
		failures = append(failures, guardPathError("close lock", guard.path, err))
	}
	return errors.Join(failures...)
}

func prepareGuardDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return guardPathError("inspect runtime directory", path, errors.New("target is not a non-symlink directory"))
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return guardPathError("create runtime directory", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return guardPathError("inspect runtime directory", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return guardPathError("inspect runtime directory", path, errors.New("target is not a non-symlink directory"))
		}
	} else {
		return guardPathError("inspect runtime directory", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return guardPathError("secure runtime directory", path, err)
	}
	return nil
}

func guardPathError(operation, path string, err error) error {
	return fmt.Errorf("database operation guard: %s %s: %w; check the path and permissions, then retry", operation, path, err)
}
