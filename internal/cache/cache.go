// Package cache provides the bounded Rust-compatible provider payload cache.
package cache

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	magic           = "skout-cache-v1\n"
	MaxPayloadBytes = 32 * 1024 * 1024
	maxEntryBytes   = MaxPayloadBytes + 128
	pruneAge        = 24 * time.Hour
)

// Clock supplies wall time to deterministic cache operations.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Entry is one complete cached payload and capture time.
type Entry struct {
	FetchedAt time.Time
	Payload   []byte
}

// State describes a cache lookup disposition.
type State uint8

const (
	Missing State = iota
	Hit
	Expired
	Corrupt
)

type writeStage uint8

const (
	beforeCreate writeStage = iota
	afterCreate
	afterWrite
	afterFileSync
	afterRename
)

// Lookup is the typed result of one cache lookup.
type Lookup struct {
	State  State
	Entry  Entry
	Path   string
	Reason string
}

// PruneIssue describes one file-level pruning failure.
type PruneIssue struct {
	Path   string
	Detail string
}

// PruneReport summarizes a deterministic namespace pruning pass.
type PruneReport struct {
	Scanned   int
	Removed   int
	Malformed int
	Unrelated int
	Failed    int
	Issues    []PruneIssue
}

// Disk owns one cache root and clock without eagerly creating state.
type Disk struct {
	root  string
	clock Clock
}

// Production constructs the production cache without creating it.
func Production() (*Disk, error) {
	root, err := ProductionRoot()
	if err != nil {
		return nil, err
	}
	return At(root), nil
}

// At constructs a cache at an explicit root.
func At(root string) *Disk { return WithClock(root, systemClock{}) }

// WithClock constructs a cache at an explicit root with controlled time.
func WithClock(root string, clock Clock) *Disk { return &Disk{root: root, clock: clock} }

// Root returns the selected cache root.
func (cache *Disk) Root() string { return cache.root }

// ProductionRoot resolves the production cache root without creating it.
func ProductionRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		home, ok := os.LookupEnv("HOME")
		if !ok || home == "" {
			return "", fmt.Errorf("resolve cache root: platform cache directory and home directory are unavailable; set HOME and retry")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "skout", "api-cache"), nil
}

// EntryPath returns the deterministic path for a logical cache entry.
func (cache *Disk) EntryPath(namespace, key string) (string, error) {
	if err := validateComponent("derive cache path", "namespace", namespace, 64); err != nil {
		return "", err
	}
	if err := validateComponent("derive cache path", "key", key, 256); err != nil {
		return "", err
	}
	return filepath.Join(cache.root, namespace, filename(namespace, key)), nil
}

// Get reads one entry under a positive TTL.
func (cache *Disk) Get(namespace, key string, ttl time.Duration) (Lookup, error) {
	const operation = "read cache entry"
	if err := validateComponent(operation, "namespace", namespace, 64); err != nil {
		return Lookup{}, err
	}
	if err := validateComponent(operation, "key", key, 256); err != nil {
		return Lookup{}, err
	}
	if ttl <= 0 {
		return Lookup{}, invalid(operation, "TTL must be positive")
	}
	now := cache.clock.Now()
	if err := validateClock(now, operation); err != nil {
		return Lookup{}, err
	}
	directory := filepath.Join(cache.root, namespace)
	exists, err := inspectDirectory(cache.root)
	if err != nil || !exists {
		return Lookup{State: Missing}, err
	}
	exists, err = inspectDirectory(directory)
	if err != nil || !exists {
		return Lookup{State: Missing}, err
	}
	path := filepath.Join(directory, filename(namespace, key))
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Lookup{State: Missing}, nil
	}
	if err != nil {
		return Lookup{}, pathError(operation, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Lookup{}, invalid(operation, "cache path is not a regular non-symlink file")
	}
	if info.Size() > maxEntryBytes {
		return Lookup{State: Corrupt, Path: path, Reason: "cache entry exceeds the 32 MiB payload bound"}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Lookup{}, pathError(operation, path, err)
	}
	entry, reason := decode(data)
	if reason != "" {
		return Lookup{State: Corrupt, Path: path, Reason: reason}, nil
	}
	state := Hit
	if now.Sub(entry.FetchedAt) >= ttl {
		state = Expired
	}
	return Lookup{State: state, Entry: entry, Path: path}, nil
}

// Put atomically stores one bounded payload.
func (cache *Disk) Put(namespace, key string, payload []byte) error {
	return cache.putInner(namespace, key, payload, func(writeStage) error { return nil })
}

func (cache *Disk) putInner(namespace, key string, payload []byte, stage func(writeStage) error) error {
	const operation = "write cache entry"
	if err := validateComponent(operation, "namespace", namespace, 64); err != nil {
		return err
	}
	if err := validateComponent(operation, "key", key, 256); err != nil {
		return err
	}
	if len(payload) > MaxPayloadBytes {
		return invalid(operation, "payload exceeds 32 MiB")
	}
	now := cache.clock.Now()
	if err := validateClock(now, operation); err != nil {
		return err
	}
	directory := filepath.Join(cache.root, namespace)
	if err := prepareDirectory(cache.root); err != nil {
		return err
	}
	if err := prepareDirectory(directory); err != nil {
		return err
	}
	target := filepath.Join(directory, filename(namespace, key))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return invalid("inspect cache target", "target is not a regular non-symlink file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pathError("inspect cache target", target, err)
	}
	if err := stage(beforeCreate); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".skout-cache-*.tmp")
	if err != nil {
		return pathError("create cache temporary file", target, err)
	}
	temporary := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return pathError("set cache permissions", temporary, err)
	}
	if err := stage(afterCreate); err != nil {
		cleanup()
		return err
	}
	if _, err := file.Write(encode(now.Unix(), payload)); err != nil {
		cleanup()
		return pathError("write cache temporary file", temporary, err)
	}
	if err := stage(afterWrite); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return pathError("sync cache temporary file", temporary, err)
	}
	if err := stage(afterFileSync); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return pathError("close cache temporary file", temporary, err)
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return pathError("replace cache entry", target, err)
	}
	if err := stage(afterRename); err != nil {
		return fmt.Errorf("sync cache directory after replacing %s: %v; the new entry is visible but durability is uncertain", target, err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("sync cache directory after replacing %s: %v; the new entry is visible but durability is uncertain", target, err)
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil {
		return fmt.Errorf("sync cache directory after replacing %s: %v; the new entry is visible but durability is uncertain", target, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close cache directory after replacing %s: %v; the new entry is visible but durability is uncertain", target, closeErr)
	}
	return nil
}

// Prune removes owned entries at least 24 hours old in deterministic order.
func (cache *Disk) Prune(namespace string) (PruneReport, error) {
	const operation = "prune cache namespace"
	if err := validateComponent(operation, "namespace", namespace, 64); err != nil {
		return PruneReport{}, err
	}
	now := cache.clock.Now()
	if err := validateClock(now, operation); err != nil {
		return PruneReport{}, err
	}
	directory := filepath.Join(cache.root, namespace)
	exists, err := inspectDirectory(cache.root)
	if err != nil || !exists {
		return PruneReport{}, err
	}
	exists, err = inspectDirectory(directory)
	if err != nil || !exists {
		return PruneReport{}, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return PruneReport{}, pathError(operation, directory, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var report PruneReport
	for _, directoryEntry := range entries {
		report.Scanned++
		name := directoryEntry.Name()
		path := filepath.Join(directory, name)
		if !ownedFilename(name) {
			report.Unrelated++
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			reportFailure(&report, path, err)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			report.Unrelated++
			continue
		}
		if info.Size() > maxEntryBytes {
			report.Malformed++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			reportFailure(&report, path, err)
			continue
		}
		entry, reason := decode(data)
		if reason != "" {
			report.Malformed++
			continue
		}
		if now.Sub(entry.FetchedAt) >= pruneAge {
			if err := os.Remove(path); err != nil {
				reportFailure(&report, path, err)
			} else {
				report.Removed++
			}
		}
	}
	return report, nil
}

func encode(timestamp int64, payload []byte) []byte {
	header := magic + strconv.FormatInt(timestamp, 10) + "\n" + strconv.Itoa(len(payload)) + "\n"
	return append([]byte(header), payload...)
}

func decode(data []byte) (Entry, string) {
	text := string(data)
	if !strings.HasPrefix(text, magic) {
		return Entry{}, "unknown cache format"
	}
	rest := text[len(magic):]
	timestampLine, rest, found := strings.Cut(rest, "\n")
	if !found {
		return Entry{}, "missing cache timestamp"
	}
	lengthLine, _, found := strings.Cut(rest, "\n")
	if !found {
		return Entry{}, "missing cache payload length"
	}
	payloadOffset := len(magic) + len(timestampLine) + 1 + len(lengthLine) + 1
	timestamp, err := strconv.ParseInt(timestampLine, 10, 64)
	if err != nil || timestamp <= 0 || strconv.FormatInt(timestamp, 10) != timestampLine {
		return Entry{}, "cache timestamp is invalid or noncanonical"
	}
	length, err := strconv.Atoi(lengthLine)
	if err != nil || length < 0 || strconv.Itoa(length) != lengthLine || length > MaxPayloadBytes || len(data)-payloadOffset != length {
		return Entry{}, "cache payload length is invalid"
	}
	payload := append([]byte(nil), data[payloadOffset:]...)
	return Entry{FetchedAt: time.Unix(timestamp, 0), Payload: payload}, ""
}

func filename(namespace, key string) string {
	digest := sha256.Sum256(append(append([]byte(namespace), 0), []byte(key)...))
	return fmt.Sprintf("skoutc-%x.cache", digest)
}

func validateComponent(operation, field, value string, maximum int) error {
	valid := value != "" && len(value) <= maximum && value != "." && value != ".."
	for _, character := range []byte(value) {
		valid = valid && (character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-_.", rune(character)))
	}
	if !valid {
		return invalid(operation, fmt.Sprintf("%s must contain 1 through %d portable ASCII characters", field, maximum))
	}
	return nil
}

func prepareDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return invalid("prepare cache directory", path+" is not a non-symlink directory")
		}
		return os.Chmod(path, 0o700)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pathError("inspect cache directory", path, err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return pathError("create cache directory", path, err)
	}
	return os.Chmod(path, 0o700)
}

func inspectDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, pathError("inspect cache namespace", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, invalid("inspect cache namespace", path+" is not a non-symlink directory")
	}
	return true, nil
}

func ownedFilename(name string) bool {
	if len(name) != 77 || !strings.HasPrefix(name, "skoutc-") || !strings.HasSuffix(name, ".cache") {
		return false
	}
	for _, character := range name[7:71] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validateClock(now time.Time, operation string) error {
	if now.Before(time.Unix(1, 0)) {
		return invalid(operation, "clock must be after the Unix epoch")
	}
	return nil
}

func reportFailure(report *PruneReport, path string, err error) {
	report.Failed++
	report.Issues = append(report.Issues, PruneIssue{Path: path, Detail: err.Error()})
}

func invalid(operation, detail string) error {
	return fmt.Errorf("%s: %s; correct the value and retry", operation, detail)
}

func pathError(operation, path string, err error) error {
	return fmt.Errorf("%s %s: %v; check the cache path and permissions, then retry", operation, path, err)
}
