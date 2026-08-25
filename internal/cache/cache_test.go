package cache

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type sequenceClock struct {
	values []time.Time
	calls  int
}

func (clock *sequenceClock) Now() time.Time {
	value := clock.values[min(clock.calls, len(clock.values)-1)]
	clock.calls++
	return value
}

func TestDiskCacheEncodingTTLPermissionsAndCorruption(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	root := filepath.Join(t.TempDir(), "cache")
	disk := WithClock(root, fixedClock{now})
	payload := []byte{0, 1, 2, 0xff}
	lookup, err := disk.Get("mlb", "schedule-2026-08-25", time.Minute)
	if err != nil || lookup.State != Missing {
		t.Fatalf("missing lookup = %#v, %v", lookup, err)
	}
	if err := disk.Put("mlb", "schedule-2026-08-25", payload); err != nil {
		t.Fatal(err)
	}
	lookup, err = disk.Get("mlb", "schedule-2026-08-25", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.State != Hit || !bytes.Equal(lookup.Entry.Payload, payload) {
		t.Fatalf("lookup = %#v", lookup)
	}
	data, err := os.ReadFile(lookup.Path)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "mlb", "skoutc-245c1f4e779eb57a82d4a226fef8d223e801151680a33ed621866a7ca25997cb.cache")
	if lookup.Path != wantPath {
		t.Fatalf("path=%q want=%q", lookup.Path, wantPath)
	}
	wantData := append([]byte("skout-cache-v1\n2000000000\n4\n"), payload...)
	if !bytes.Equal(data, wantData) {
		t.Fatalf("encoding=%q want=%q", data, wantData)
	}
	info, _ := os.Stat(lookup.Path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode=%o", info.Mode().Perm())
	}
	for _, directory := range []string{root, filepath.Join(root, "mlb")} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("directory %s mode=%o", directory, info.Mode().Perm())
		}
	}
	expired := WithClock(root, fixedClock{now.Add(time.Minute)})
	lookup, err = expired.Get("mlb", "schedule-2026-08-25", time.Minute)
	if err != nil || lookup.State != Expired {
		t.Fatalf("TTL boundary = %#v, %v", lookup, err)
	}
	if err := os.WriteFile(lookup.Path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup, err = disk.Get("mlb", "schedule-2026-08-25", time.Minute)
	if err != nil || lookup.State != Corrupt {
		t.Fatalf("corrupt = %#v, %v", lookup, err)
	}
}

func TestDiskCacheRejectsBoundsAndSymlinksAndConcurrentWritesAreComplete(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	root := filepath.Join(t.TempDir(), "cache")
	disk := WithClock(root, fixedClock{now})
	if err := disk.Put("mlb", "huge", make([]byte, MaxPayloadBytes+1)); err == nil {
		t.Fatal("oversize Put succeeded")
	}
	if err := os.MkdirAll(filepath.Join(root, "mlb"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, _ := disk.EntryPath("mlb", "linked")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := disk.Put("mlb", "linked", []byte("no")); err == nil {
		t.Fatal("symlink Put succeeded")
	}
	if _, err := disk.Get("mlb", "linked", time.Minute); err == nil {
		t.Fatal("symlink Get succeeded")
	}
	realNamespace := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(linkedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realNamespace, filepath.Join(linkedRoot, "mlb")); err != nil {
		t.Fatal(err)
	}
	linkedNamespace := WithClock(linkedRoot, fixedClock{now})
	if _, err := linkedNamespace.Get("mlb", "key", time.Minute); err == nil {
		t.Fatal("symlink namespace Get succeeded")
	}
	if err := linkedNamespace.Put("mlb", "key", []byte("value")); err == nil {
		t.Fatal("symlink namespace Put succeeded")
	}
	realRoot := t.TempDir()
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	linkedCache := WithClock(rootLink, fixedClock{now})
	if _, err := linkedCache.Get("mlb", "key", time.Minute); err == nil {
		t.Fatal("symlink root Get succeeded")
	}
	if err := linkedCache.Put("mlb", "key", []byte("value")); err == nil {
		t.Fatal("symlink root Put succeeded")
	}
	var group sync.WaitGroup
	writeErrors := make(chan error, 8)
	for index := range 8 {
		group.Add(1)
		go func(value byte) {
			defer group.Done()
			writeErrors <- disk.Put("mlb", "race", bytes.Repeat([]byte{value}, 4096))
		}(byte(index))
	}
	group.Wait()
	close(writeErrors)
	for err := range writeErrors {
		if err != nil {
			t.Errorf("concurrent Put: %v", err)
		}
	}
	lookup, err := disk.Get("mlb", "race", time.Hour)
	if err != nil || lookup.State != Hit || len(lookup.Entry.Payload) != 4096 {
		t.Fatalf("concurrent lookup=%#v err=%v", lookup, err)
	}
	for _, value := range lookup.Entry.Payload {
		if value != lookup.Entry.Payload[0] {
			t.Fatal("concurrent payload was torn")
		}
	}
}

func TestDiskCacheAtomicReplacementFailureStages(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	root := filepath.Join(t.TempDir(), "cache")
	disk := WithClock(root, fixedClock{now})
	if err := disk.Put("mlb", "key", []byte("old")); err != nil {
		t.Fatal(err)
	}
	for _, rejected := range []writeStage{beforeCreate, afterCreate, afterWrite, afterFileSync} {
		err := disk.putInner("mlb", "key", []byte("new"), func(stage writeStage) error {
			if stage == rejected {
				return errors.New("injected failure")
			}
			return nil
		})
		if err == nil {
			t.Fatalf("stage %d succeeded", rejected)
		}
		lookup, getErr := disk.Get("mlb", "key", time.Minute)
		if getErr != nil || lookup.State != Hit || string(lookup.Entry.Payload) != "old" {
			t.Fatalf("stage %d lookup=%#v err=%v", rejected, lookup, getErr)
		}
	}
	err := disk.putInner("mlb", "key", []byte("new"), func(stage writeStage) error {
		if stage == afterRename {
			return errors.New("injected failure")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "durability is uncertain") {
		t.Fatalf("after-rename error=%v", err)
	}
	lookup, getErr := disk.Get("mlb", "key", time.Minute)
	if getErr != nil || lookup.State != Hit || string(lookup.Entry.Payload) != "new" {
		t.Fatalf("after-rename lookup=%#v err=%v", lookup, getErr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "mlb"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}

func TestDiskCacheCorruptionMatrix(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	cases := map[string][]byte{
		"unknown format":    []byte("bad"),
		"missing timestamp": []byte(magic),
		"missing length":    []byte(magic + "2000000000\n"),
		"zero timestamp":    []byte(magic + "0\n0\n"),
		"leading timestamp": []byte(magic + "02000000000\n0\n"),
		"leading length":    []byte(magic + "2000000000\n01\nx"),
		"length mismatch":   []byte(magic + "2000000000\n2\nx"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "cache")
			disk := WithClock(root, fixedClock{now})
			path, err := disk.EntryPath("mlb", "key")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			lookup, err := disk.Get("mlb", "key", time.Minute)
			if err != nil || lookup.State != Corrupt || lookup.Reason == "" {
				t.Fatalf("lookup=%#v err=%v", lookup, err)
			}
		})
	}
	t.Run("oversized file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "cache")
		disk := WithClock(root, fixedClock{now})
		path, _ := disk.EntryPath("mlb", "key")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxEntryBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		lookup, err := disk.Get("mlb", "key", time.Minute)
		if err != nil || lookup.State != Corrupt || !strings.Contains(lookup.Reason, "32 MiB") {
			t.Fatalf("lookup=%#v err=%v", lookup, err)
		}
	})
}

func TestDiskCacheCapturesLookupClockOnce(t *testing.T) {
	storedAt := time.Unix(2_000_000_000, 0)
	root := filepath.Join(t.TempDir(), "cache")
	if err := WithClock(root, fixedClock{storedAt}).Put("mlb", "clock", []byte("value")); err != nil {
		t.Fatal(err)
	}
	clock := &sequenceClock{values: []time.Time{storedAt.Add(59 * time.Second), storedAt.Add(61 * time.Second)}}
	lookup, err := WithClock(root, clock).Get("mlb", "clock", time.Minute)
	if err != nil || lookup.State != Hit {
		t.Fatalf("lookup=%#v err=%v", lookup, err)
	}
	if clock.calls != 1 {
		t.Fatalf("clock calls=%d want=1", clock.calls)
	}
}

func TestDiskCachePruneIsOwnedAndDeterministic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	old := time.Unix(2_000_000_000, 0)
	disk := WithClock(root, fixedClock{old})
	if err := disk.Put("mlb", "old", []byte("x")); err != nil {
		t.Fatal(err)
	}
	current := WithClock(root, fixedClock{old.Add(23*time.Hour + 59*time.Minute)})
	if err := current.Put("mlb", "current", []byte("y")); err != nil {
		t.Fatal(err)
	}
	malformed, _ := disk.EntryPath("mlb", "malformed")
	if err := os.WriteFile(malformed, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked, _ := disk.EntryPath("mlb", "linked")
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), linked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mlb", "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := WithClock(root, fixedClock{old.Add(24 * time.Hour)}).Prune("mlb")
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned != 5 || report.Removed != 1 || report.Malformed != 1 || report.Unrelated != 2 || report.Failed != 0 || len(report.Issues) != 0 {
		t.Fatalf("report=%#v", report)
	}
	for _, path := range []string{filepath.Join(root, "mlb", "notes.txt"), malformed, linked} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("preserved path %s: %v", path, err)
		}
	}
	if lookup, err := current.Get("mlb", "current", time.Hour); err != nil || lookup.State != Hit {
		t.Fatalf("current lookup=%#v err=%v", lookup, err)
	}
	if _, err := disk.EntryPath("../bad", "x"); err == nil || !strings.Contains(err.Error(), "portable ASCII") {
		t.Fatalf("invalid namespace error=%v", err)
	}
}
