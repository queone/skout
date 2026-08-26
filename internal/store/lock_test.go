package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseGuardSharedExclusiveLifecycleAndPermissions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "private", "skout.db")
	first, err := AcquireDatabaseGuard(databasePath, DatabaseGuardShared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AcquireDatabaseGuard(databasePath, DatabaseGuardShared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDatabaseGuard(databasePath, DatabaseGuardExclusive); err == nil || !strings.Contains(err.Error(), "another skout command") {
		t.Fatalf("exclusive during shared guards: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDatabaseGuard(databasePath, DatabaseGuardExclusive); err == nil {
		t.Fatal("exclusive guard acquired while second shared guard remained")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err := AcquireDatabaseGuard(databasePath, DatabaseGuardExclusive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDatabaseGuard(databasePath, DatabaseGuardShared); err == nil || !strings.Contains(err.Error(), "another skout command") {
		t.Fatalf("shared during exclusive guard: %v", err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := AcquireDatabaseGuard(databasePath, DatabaseGuardShared)
	if err != nil {
		t.Fatal(err)
	}
	defer reacquired.Close()
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("guard created database: %v", err)
	}
	for target, want := range map[string]os.FileMode{
		filepath.Join(filepath.Dir(databasePath), "runtime"):                  0o700,
		filepath.Join(filepath.Dir(databasePath), "runtime", "database.lock"): 0o600,
	} {
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
			t.Errorf("%s mode=%v want=%o", target, info.Mode(), want)
		}
	}
}

func TestDatabaseGuardRejectsUnsafePathsAndInvalidInputs(t *testing.T) {
	if _, err := AcquireDatabaseGuard("", DatabaseGuardShared); err == nil {
		t.Fatal("empty database path succeeded")
	}
	if _, err := AcquireDatabaseGuard(filepath.Join(t.TempDir(), "skout.db"), 0); err == nil {
		t.Fatal("invalid guard mode succeeded")
	}

	t.Run("runtime symlink", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(root, "runtime")); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireDatabaseGuard(filepath.Join(root, "skout.db"), DatabaseGuardShared); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
			t.Fatalf("runtime symlink result: %v", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		root := t.TempDir()
		runtimeDirectory := filepath.Join(root, "runtime")
		if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(runtimeDirectory, "database.lock")); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireDatabaseGuard(filepath.Join(root, "skout.db"), DatabaseGuardExclusive); err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
			t.Fatalf("lock symlink result: %v", err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "keep" {
			t.Fatalf("symlink target data=%q err=%v", data, err)
		}
	})

	t.Run("lock directory", func(t *testing.T) {
		root := t.TempDir()
		lockPath := filepath.Join(root, "runtime", "database.lock")
		if err := os.MkdirAll(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireDatabaseGuard(filepath.Join(root, "skout.db"), DatabaseGuardExclusive); err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
			t.Fatalf("lock directory result: %v", err)
		}
	})
}
