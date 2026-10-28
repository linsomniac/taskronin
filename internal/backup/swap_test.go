package backup_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/backup"
)

var errSwapCrash = errors.New("simulated swap crash")

func TestSwapHappyPath(t *testing.T) {
	path, j := swapFixture(t, backup.SwapRestore)
	calls := 0
	var points []string
	ctx := backup.WithStepHook(t.Context(), func(point string) error {
		points = append(points, point)
		return nil
	})
	err := backup.Swap(ctx, path, j, func() error {
		calls++
		requireSwapFile(t, j.Target, "NEW")
		got := requireSwapJournal(t, path)
		if got.Step != "installed" || !got.OwnerTokenPending {
			t.Fatalf("callback journal = %+v", got)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("Swap = %v; callback calls = %d", err, calls)
	}
	if !slices.Equal(points, swapPoints(backup.SwapRestore)) {
		t.Fatalf("hook order = %v", points)
	}
	requireSwapInstalled(t, path, j)
	info, err := os.Stat(j.Aside)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("aside mode: %v, %v", info, err)
	}
}

func TestSwapCrashThenResume(t *testing.T) {
	for _, kind := range []backup.SwapKind{backup.SwapRestore, backup.SwapImport, backup.SwapScrub} {
		for _, point := range swapPoints(kind) {
			t.Run(string(kind)+"/"+point, func(t *testing.T) {
				path, j := swapFixture(t, kind)
				calls := 0
				err := backup.Swap(swapCrashAt(t.Context(), point), path, j, func() error { calls++; return nil })
				requireSwapCrash(t, err)
				got := requireSwapJournal(t, path)
				wantStep := strings.TrimSuffix(point, ":done")
				switch point {
				case "moved-old:done":
					wantStep = "prepared"
				case "installed:done":
					wantStep = "moved-old"
				case "on-installed", "aside-deleted:done":
					wantStep = "installed"
				}
				if got.Step != wantStep || got.Kind != kind || got.OwnerTokenPending != j.OwnerTokenPending {
					t.Fatalf("journal at %s = %+v", point, got)
				}
				beforeCalls := calls
				if err := backup.ResumeSwap(t.Context(), path); err != nil {
					t.Fatal(err)
				}
				if calls != beforeCalls {
					t.Fatal("resume called onInstalled")
				}
				requireSwapInstalled(t, path, j)
			})
		}
	}
}

func TestSwapCrashThenRollback(t *testing.T) {
	for _, kind := range []backup.SwapKind{backup.SwapRestore, backup.SwapImport} {
		for _, point := range swapPoints(kind) {
			t.Run(string(kind)+"/"+point, func(t *testing.T) {
				path, j := swapFixture(t, kind)
				requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), point), path, j, nil))
				if err := backup.RollbackSwap(t.Context(), path); err != nil {
					t.Fatal(err)
				}
				requireSwapRolledBack(t, path, j)
			})
		}
	}
}

func TestRollbackCrashThenRollbackAgain(t *testing.T) {
	for _, point := range []string{
		"rollback:started", "rollback:discarding", "rollback:new-discarded:done", "rollback:new-discarded",
		"rollback:moved:taskronin.db", "rollback:moved:taskronin.db-wal", "rollback:moved:taskronin.db-shm",
		"rollback:old-restored",
	} {
		t.Run(point, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapRestore)
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed"), path, j, nil))
			requireSwapCrash(t, backup.RollbackSwap(swapCrashAt(t.Context(), point), path))
			got := requireSwapJournal(t, path)
			if got.Rollback == "" {
				t.Fatal("rollback progress not journaled")
			}
			if point == "rollback:discarding" || point == "rollback:new-discarded:done" {
				if got.Rollback != "discarding" || filepath.Dir(got.Discard) != filepath.Dir(j.Target) ||
					!regexp.MustCompile(`^\.tkr-tmp-rollback-[0-9a-f]{16}$`).MatchString(filepath.Base(got.Discard)) {
					t.Fatalf("discard intent = %+v", got)
				}
				if point == "rollback:discarding" {
					requireSwapAbsent(t, got.Discard)
					requireSwapFile(t, j.Target, "NEW")
				} else {
					requireSwapFile(t, got.Discard, "NEW")
					requireSwapAbsent(t, j.Target)
				}
			}
			before := swapTree(t, filepath.Dir(path))
			err := backup.ResumeSwap(t.Context(), path)
			if !errors.Is(err, backup.ErrRollbackInProgress) || !strings.Contains(err.Error(), "run `tkr restore --rollback`") {
				t.Fatalf("resume during rollback = %v", err)
			}
			if after := swapTree(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
				t.Fatal("refused resume changed disk state")
			}
			if err := backup.RollbackSwap(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			requireSwapRolledBack(t, path, j)
			if got.Discard != "" {
				requireSwapAbsent(t, got.Discard)
			}
		})
	}
}

func TestRollbackScrubRefused(t *testing.T) {
	for _, point := range swapPoints(backup.SwapScrub) {
		t.Run(point, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapScrub)
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), point), path, j, nil))
			before := swapTree(t, filepath.Dir(path))
			if err := backup.RollbackSwap(t.Context(), path); !errors.Is(err, backup.ErrNoRollback) {
				t.Fatalf("scrub rollback = %v", err)
			}
			if after := swapTree(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
				t.Fatal("refused scrub rollback changed disk state")
			}
		})
	}
}

func TestResumeNoJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swap.journal")
	if _, err := backup.ReadJournal(path); !errors.Is(err, backup.ErrNoJournal) {
		t.Fatalf("ReadJournal = %v", err)
	}
	if err := backup.ResumeSwap(t.Context(), path); !errors.Is(err, backup.ErrNoJournal) {
		t.Fatalf("ResumeSwap = %v", err)
	}
	if err := backup.RollbackSwap(t.Context(), path); !errors.Is(err, backup.ErrNoJournal) {
		t.Fatalf("RollbackSwap = %v", err)
	}
}

func TestJournalMode(t *testing.T) {
	path, j := swapFixture(t, backup.SwapScrub)
	ctx := backup.WithStepHook(t.Context(), func(point string) error {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("journal mode at %s: %v, %v", point, info, err)
		}
		return nil
	})
	if err := backup.Swap(ctx, path, j, nil); err != nil {
		t.Fatal(err)
	}
}

func TestJournalEncoding(t *testing.T) {
	path, j := swapFixture(t, backup.SwapRestore)
	requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "prepared"), path, j, nil))
	got := requireSwapJournal(t, path)
	if got.Version != 1 || got.StartedAt != j.StartedAt || got.New != j.New || got.Target != j.Target || got.Aside != j.Aside {
		t.Fatalf("journal round trip = %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"version", "kind", "step", "new", "target", "aside", "delete_aside", "owner_token_pending", "started_at", "rollback", "discard"}
	if len(fields) != len(want) {
		t.Fatalf("journal fields = %s", data)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Errorf("journal missing %s", name)
		}
	}
}

func TestSwapPartialOldMove(t *testing.T) {
	for _, recoverWith := range []string{"resume", "rollback"} {
		for moved := 1; moved <= 2; moved++ {
			t.Run(fmt.Sprintf("%s/%d", recoverWith, moved), func(t *testing.T) {
				path, j := swapFixture(t, backup.SwapRestore)
				requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "prepared"), path, j, nil))
				// Model a death between individual forward renames, before the
				// moved-old:done hook. Recovery itself uses only the public API.
				if err := os.Mkdir(j.Aside, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{"", "-wal"}[:moved] {
					if err := os.Rename(j.Target+suffix, filepath.Join(j.Aside, filepath.Base(j.Target)+suffix)); err != nil {
						t.Fatal(err)
					}
				}
				if recoverWith == "resume" {
					if err := backup.ResumeSwap(t.Context(), path); err != nil {
						t.Fatal(err)
					}
					requireSwapInstalled(t, path, j)
				} else {
					if err := backup.RollbackSwap(t.Context(), path); err != nil {
						t.Fatal(err)
					}
					requireSwapRolledBack(t, path, j)
				}
			})
		}
	}
}

func TestSwapWithoutSidecars(t *testing.T) {
	for _, recoverWith := range []string{"resume", "rollback"} {
		t.Run(recoverWith, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapRestore)
			for _, suffix := range []string{"-wal", "-shm"} {
				if err := os.Remove(j.Target + suffix); err != nil {
					t.Fatal(err)
				}
			}
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed:done"), path, j, nil))
			if recoverWith == "resume" {
				if err := backup.ResumeSwap(t.Context(), path); err != nil {
					t.Fatal(err)
				}
				requireSwapFile(t, j.Target, "NEW")
				requireSwapFile(t, filepath.Join(j.Aside, "taskronin.db"), "OLD")
			} else {
				if err := backup.RollbackSwap(t.Context(), path); err != nil {
					t.Fatal(err)
				}
				requireSwapFile(t, j.Target, "OLD")
				requireSwapAbsent(t, j.Aside)
			}
			requireSwapAbsent(t, path, j.Target+"-wal", j.Target+"-shm")
		})
	}
}

func TestSwapCallbackFailure(t *testing.T) {
	path, j := swapFixture(t, backup.SwapRestore)
	callbackErr := errors.New("owner token write failed")
	if err := backup.Swap(t.Context(), path, j, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("callback failure = %v", err)
	}
	got := requireSwapJournal(t, path)
	if got.Step != "installed" || !got.OwnerTokenPending {
		t.Fatalf("failed callback journal = %+v", got)
	}
	if err := backup.ResumeSwap(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	requireSwapInstalled(t, path, j)
}

func TestSwapCanceled(t *testing.T) {
	for _, point := range append([]string{"before"}, swapPoints(backup.SwapScrub)...) {
		t.Run(point, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapScrub)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if point == "before" {
				cancel()
			}
			ctx = backup.WithStepHook(ctx, func(at string) error {
				if at == point {
					cancel()
				}
				return nil
			})
			if err := backup.Swap(ctx, path, j, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled swap = %v", err)
			}
			if point == "before" {
				requireSwapAbsent(t, path, j.Aside)
				requireSwapFile(t, j.Target, "OLD")
				return
			}
			requireSwapJournal(t, path)
			if err := backup.ResumeSwap(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			requireSwapInstalled(t, path, j)
		})
	}
}

func TestSwapPreservesOutstandingJournal(t *testing.T) {
	path, j := swapFixture(t, backup.SwapRestore)
	requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed"), path, j, nil))
	before := swapTree(t, filepath.Dir(path))
	next := *j
	next.Step = ""
	if err := backup.Swap(t.Context(), path, &next, nil); err == nil {
		t.Fatal("overwrote an outstanding journal")
	}
	if after := swapTree(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
		t.Fatal("second swap changed disk state")
	}
}

func TestResumePartiallyDeletedAside(t *testing.T) {
	for _, remaining := range []string{"sidecars", "empty", "absent"} {
		t.Run(remaining, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapScrub)
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed"), path, j, nil))
			// Model a death during unlinking, before aside-deleted:done.
			if err := os.Remove(filepath.Join(j.Aside, "taskronin.db")); err != nil {
				t.Fatal(err)
			}
			if remaining != "sidecars" {
				for _, member := range []string{"taskronin.db-wal", "taskronin.db-shm"} {
					if err := os.Remove(filepath.Join(j.Aside, member)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if remaining == "absent" {
				if err := os.Remove(j.Aside); err != nil {
					t.Fatal(err)
				}
			}
			if err := backup.ResumeSwap(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			requireSwapInstalled(t, path, j)
		})
	}
}

func TestRollbackPartiallyCleaned(t *testing.T) {
	for _, removeAside := range []bool{false, true} {
		t.Run(fmt.Sprintf("aside-removed=%t", removeAside), func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapRestore)
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed"), path, j, nil))
			requireSwapCrash(t, backup.RollbackSwap(swapCrashAt(t.Context(), "rollback:old-restored"), path))
			got := requireSwapJournal(t, path)
			// Model a death after deleting Discard, and optionally Aside,
			// before removing the old-restored journal.
			if err := os.Remove(got.Discard); err != nil {
				t.Fatal(err)
			}
			if removeAside {
				if err := os.Remove(j.Aside); err != nil {
					t.Fatal(err)
				}
			}
			if err := backup.RollbackSwap(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			requireSwapRolledBack(t, path, j)
		})
	}
}

func TestJournalInvalidRefused(t *testing.T) {
	for _, damage := range []string{"json", "version", "kind", "step", "rollback", "discard", "empty-target"} {
		t.Run(damage, func(t *testing.T) {
			path, j := swapFixture(t, backup.SwapRestore)
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "installed"), path, j, nil))
			got := requireSwapJournal(t, path)
			switch damage {
			case "version":
				got.Version = 2
			case "kind":
				got.Kind = "unknown"
			case "step":
				got.Step = "unknown"
			case "rollback":
				got.Rollback = "unknown"
			case "discard":
				got.Rollback, got.Discard = "old-restored", j.Target
			case "empty-target":
				got.Target = ""
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "json" {
				data = []byte("{")
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := swapTree(t, filepath.Dir(path))
			if _, err := backup.ReadJournal(path); err == nil || errors.Is(err, backup.ErrNoJournal) {
				t.Fatalf("damaged journal read = %v", err)
			}
			if err := backup.ResumeSwap(t.Context(), path); err == nil {
				t.Fatal("resumed an invalid journal")
			}
			if err := backup.RollbackSwap(t.Context(), path); err == nil {
				t.Fatal("rolled back an invalid journal")
			}
			if after := swapTree(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
				t.Fatal("invalid journal recovery changed disk state")
			}
		})
	}
}

func swapFixture(t *testing.T, kind backup.SwapKind) (string, *backup.SwapJournal) {
	t.Helper()
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	j := &backup.SwapJournal{
		Kind: kind, New: filepath.Join(staging, "new.db"), Target: filepath.Join(dir, "taskronin.db"),
		DeleteAside: kind == backup.SwapScrub, OwnerTokenPending: kind == backup.SwapRestore,
		StartedAt: time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC),
	}
	j.Aside = j.Target + ".pre-" + string(kind) + "-20261028T160000Z/"
	if kind == backup.SwapScrub {
		j.Aside = filepath.Join(dir, ".tkr-tmp-quarantine-test")
	}
	for path, data := range map[string]string{j.Target: "OLD", j.Target + "-wal": "OLDWAL", j.Target + "-shm": "OLDSHM", j.New: "NEW"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	name := "swap.journal"
	if kind == backup.SwapScrub {
		name = "scrub.journal"
	}
	return filepath.Join(dir, name), j
}

func swapPoints(kind backup.SwapKind) []string {
	points := []string{"prepared", "moved-old:done", "moved-old", "installed:done", "installed", "on-installed"}
	if kind == backup.SwapScrub {
		points = append(points, "aside-deleted:done", "aside-deleted")
	}
	return points
}

func swapCrashAt(ctx context.Context, point string) context.Context {
	return backup.WithStepHook(ctx, func(at string) error {
		if at == point {
			return errSwapCrash
		}
		return nil
	})
}

func requireSwapCrash(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errSwapCrash) {
		t.Fatalf("crash error = %v", err)
	}
}

func requireSwapJournal(t *testing.T, path string) *backup.SwapJournal {
	t.Helper()
	j, err := backup.ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func requireSwapInstalled(t *testing.T, path string, j *backup.SwapJournal) {
	t.Helper()
	requireSwapFile(t, j.Target, "NEW")
	requireSwapAbsent(t, path, j.New, j.Target+"-wal", j.Target+"-shm")
	if j.DeleteAside {
		requireSwapAbsent(t, j.Aside)
	} else {
		for suffix, data := range map[string]string{"": "OLD", "-wal": "OLDWAL", "-shm": "OLDSHM"} {
			requireSwapFile(t, filepath.Join(j.Aside, filepath.Base(j.Target)+suffix), data)
		}
	}
}

func requireSwapRolledBack(t *testing.T, path string, j *backup.SwapJournal) {
	t.Helper()
	for suffix, data := range map[string]string{"": "OLD", "-wal": "OLDWAL", "-shm": "OLDSHM"} {
		requireSwapFile(t, j.Target+suffix, data)
	}
	requireSwapAbsent(t, path, j.Aside)
	discard, err := filepath.Glob(filepath.Join(filepath.Dir(j.Target), ".tkr-tmp-rollback-*"))
	if err != nil || len(discard) != 0 {
		t.Fatalf("discard leftovers = %v, %v", discard, err)
	}
}

func requireSwapFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v, %v", info, err)
	}
}

func requireSwapAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}

func swapTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = info.Mode().String()
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] += string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
