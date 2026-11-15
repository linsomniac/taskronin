package service_test

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// IDs whose tests live in other plans (catalog §17). Plans B and C remove
// their IDs here when they add Test<PFX><NN>_<suffix> scenario tests.
var notPlanA = map[string]string{
	"TRI-17": "C",
}

func TestCatalogCoverage(t *testing.T) {
	ids := catalogIDs(t, "../../docs/superpowers/specs/2026-10-25-taskronin-behavior-catalog.md")
	if len(ids) != 281 {
		t.Fatalf("catalog has %d scenario ids, want 281", len(ids))
	}
	tests := testFuncNames(t, "../..")
	for _, id := range ids {
		if _, other := notPlanA[id]; other {
			continue
		}
		if !tests[strings.ReplaceAll(id, "-", "")] {
			t.Errorf("no test for %s", id)
		}
	}
}

func catalogIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile(`(?m)^\| ([A-Z]{3}-\d{2}) \|`).FindAllSubmatch(data, -1)
	ids := make([]string, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		id := string(row[1])
		if seen[id] {
			t.Fatalf("duplicate catalog scenario %s", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func testFuncNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	pattern := regexp.MustCompile(`(?m)^func Test([A-Z]{3})(\d{2})(_\w+)?\(`)
	for _, dir := range []string{"internal", "cmd"} {
		path := filepath.Join(root, dir)
		err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
			// Plan A has no cmd tree yet; errors inside an existing tree fail.
			if name == path && dir == "cmd" && os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			// AIDEV-NOTE: skip the directories the go tool ignores. testutil.StartServer
			// creates and removes .tkr-* directories inside package directories while
			// other packages' tests run, so walking into one could race its removal.
			if entry.IsDir() && name != path && ignoredByGo(entry.Name()) {
				return filepath.SkipDir
			}
			if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			for _, match := range pattern.FindAllSubmatch(data, -1) {
				names[string(match[1])+string(match[2])] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return names
}

// ignoredByGo reports whether the go tool skips a directory of this name
// when matching packages.
func ignoredByGo(dir string) bool {
	return strings.HasPrefix(dir, ".") || strings.HasPrefix(dir, "_") || dir == "testdata"
}

func TestCatalogCoverageScanner(t *testing.T) {
	for _, withCmd := range []bool{false, true} {
		name := "internal-only"
		if withCmd {
			name = "internal-and-cmd"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{
				"internal/example/scenarios_test.go": "package example\n" +
					"func TestTRI01(t *testing.T) {}\n" +
					"func TestTRI02_variant(t *testing.T) {}\n" +
					"func TestTRI03WithoutUnderscore(t *testing.T) {}\n" +
					"func TestTRI040(t *testing.T) {}\n",
				"internal/example/scenarios.go":               "func TestTRI05(t *testing.T) {}\n",
				"elsewhere/scenarios_test.go":                 "func TestTRI06(t *testing.T) {}\n",
				"internal/example/.tkr-1/scenarios_test.go":   "func TestTRI07(t *testing.T) {}\n",
				"internal/example/testdata/scenarios_test.go": "func TestTRI08(t *testing.T) {}\n",
				"internal/example/_scratch/scenarios_test.go": "func TestTRI09(t *testing.T) {}\n",
			}
			want := map[string]bool{"TRI01": true, "TRI02": true}
			if withCmd {
				files["cmd/example/scenarios_test.go"] = "func TestTRI17_cli(t *testing.T) {}\n"
				want["TRI17"] = true
			}
			for path, contents := range files {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := testFuncNames(t, root); !maps.Equal(got, want) {
				t.Fatalf("scenario tests = %v, want %v", got, want)
			}
		})
	}
}
