package archtest

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// listedPackage is the subset of `go list -json` output the checker uses.
type listedPackage struct {
	ImportPath string
	Dir        string
	Imports    []string
	GoFiles    []string
}

// AIDEV-NOTE: `allowed` is copied literally from architecture A§1.3. When the
// table in the spec changes, change this map in the same commit; do not add
// edges here that the spec does not list.

// rule is one row of the A§1.3 table: allowed internal packages (names relative
// to "<module>/internal/") and allowed third-party import prefixes.
type rule struct {
	internal   []string
	thirdParty []string
}

// Third-party labels of A§1.3 mapped to import prefixes.
const (
	xAnsi      = "github.com/charmbracelet/x/ansi"
	xSysUnix   = "golang.org/x/sys/unix"
	toml       = "github.com/BurntSushi/toml"
	argon2     = "golang.org/x/crypto/argon2"
	rate       = "golang.org/x/time/rate"
	sqlite     = "modernc.org/sqlite"
	goldmark   = "github.com/yuin/goldmark"
	bluemonday = "github.com/microcosm-cc/bluemonday"
	bubbletea  = "charm.land/bubbletea/v2"
	bubbles    = "charm.land/bubbles/v2"
	lipgloss   = "charm.land/lipgloss/v2"
	goSDK      = "github.com/modelcontextprotocol/go-sdk"
	jsonSchema = "github.com/google/jsonschema-go"
	cobra      = "github.com/spf13/cobra"
	xTerm      = "golang.org/x/term"
)

var allowed = map[string]rule{
	"buildinfo": {},
	"clock":     {},
	"civil":     {},
	"datespec":  {internal: []string{"civil", "domain"}},
	"textsafe":  {thirdParty: []string{xAnsi}},
	"domain":    {internal: []string{"civil"}},
	"fsutil":    {thirdParty: []string{xSysUnix}},
	"lock":      {thirdParty: []string{xSysUnix}},
	"config":    {internal: []string{"civil", "fsutil"}, thirdParty: []string{toml}},
	"api":       {internal: []string{"domain", "civil"}},
	"auth":      {internal: []string{"domain", "clock"}, thirdParty: []string{argon2, rate}},
	"store":     {internal: []string{"domain", "civil"}, thirdParty: []string{sqlite}},
	"backup":    {internal: []string{"store", "domain", "civil", "clock", "fsutil"}},
	"service":   {internal: []string{"domain", "civil", "clock", "store", "backup", "auth", "textsafe"}},
	"markdown":  {thirdParty: []string{goldmark, bluemonday}},
	"web": {internal: []string{
		"service", "domain", "civil", "datespec", "auth", "markdown", "textsafe", "clock", "buildinfo",
	}},
	"systemd": {thirdParty: []string{xSysUnix}},
	"server": {
		internal: []string{
			"service", "api", "auth", "web", "store", "backup", "config", "lock", "fsutil",
			"systemd", "clock", "civil", "domain", "textsafe", "buildinfo",
		},
		thirdParty: []string{xSysUnix},
	},
	"client": {internal: []string{"api", "domain", "civil", "config", "fsutil", "clock"}},
	"report": {internal: []string{"api", "civil", "textsafe"}},
	"editor": {internal: []string{"fsutil", "config", "clock"}},
	"offline": {
		internal: []string{
			"store", "backup", "auth", "config", "lock", "fsutil", "systemd", "domain",
			"civil", "clock", "textsafe", "service",
		},
		thirdParty: []string{xTerm},
	},
	"tui": {
		internal:   []string{"client", "api", "domain", "civil", "datespec", "textsafe", "editor", "config"},
		thirdParty: []string{bubbletea, bubbles, lipgloss},
	},
	"mcpserver": {
		internal:   []string{"client", "api", "domain", "civil", "datespec", "config", "buildinfo"},
		thirdParty: []string{goSDK, jsonSchema},
	},
	"cli": {
		internal: []string{
			"client", "api", "domain", "civil", "datespec", "textsafe", "report", "editor",
			"config", "buildinfo", "fsutil",
		},
		thirdParty: []string{cobra, lipgloss, xTerm},
	},
}

// unchecked are test-only packages that A§1.3 exempts from the table.
var unchecked = map[string]bool{"archtest": true, "testutil": true}

// cliFileExceptions are the per-file extra internal imports of package cli
// (A§1.3). The "…" after cmd_plan.go is extended by the plan that adds another
// TUI launcher file.
var cliFileExceptions = map[string][]string{
	"cmd_serve.go":   {"server"},
	"cmd_init.go":    {"offline"},
	"cmd_migrate.go": {"offline"},
	"cmd_restore.go": {"offline"},
	"cmd_import.go":  {"offline"},
	"cmd_purge.go":   {"offline"},
	"cmd_scrub.go":   {"offline"},
	"cmd_passwd.go":  {"offline"},
	"cmd_backup.go":  {"offline"},
	"cmd_tui.go":     {"tui"},
	"cmd_plan.go":    {"tui"},
	"cmd_mcp.go":     {"mcpserver"},
}

// cmdTkrAllowed is the only internal import permitted from cmd/tkr.
var cmdTkrAllowed = []string{"cli"}

type importKind int

const (
	kindStdlib importKind = iota
	kindInternal
	kindThirdParty
)

// classify reports what kind of import imp is, relative to module.
func classify(module, imp string) importKind {
	if imp == module || strings.HasPrefix(imp, module+"/") {
		return kindInternal
	}
	first, _, _ := strings.Cut(imp, "/")
	if strings.Contains(first, ".") {
		return kindThirdParty
	}
	return kindStdlib
}

// hasPathPrefix reports whether imp is prefix or lies beneath it.
func hasPathPrefix(imp, prefix string) bool {
	return imp == prefix || strings.HasPrefix(imp, prefix+"/")
}

// internalName returns the package name relative to "<module>/internal/", or
// "" when imp is not under it.
func internalName(module, imp string) string {
	rest, ok := strings.CutPrefix(imp, module+"/internal/")
	if !ok {
		return ""
	}
	return rest
}

// importAllowed reports whether a package whose rule is r may import imp.
// extraInternal lists additional internal packages allowed (cli exceptions).
func importAllowed(module string, r rule, extraInternal []string, imp string) bool {
	switch classify(module, imp) {
	case kindStdlib:
		return true
	case kindInternal:
		name := internalName(module, imp)
		return slices.Contains(r.internal, name) || slices.Contains(extraInternal, name)
	case kindThirdParty:
		for _, p := range r.thirdParty {
			if hasPathPrefix(imp, p) {
				return true
			}
		}
	}
	return false
}

// checkPackages returns the sorted list of dependency violations in pkgs.
// fileImports returns the imports of one file of a package; it is used only
// for package cli (per-file exceptions) and may be nil when cli is absent.
func checkPackages(module string, pkgs []listedPackage, fileImports func(dir, file string) []string) []string {
	var out []string
	violation := func(from, imp string) {
		out = append(out, fmt.Sprintf("%s imports %s (not allowed by A§1.3)", from, imp))
	}
	for _, p := range pkgs {
		if p.ImportPath == module+"/cmd/tkr" {
			for _, imp := range p.Imports {
				if classify(module, imp) == kindStdlib {
					continue
				}
				name := internalName(module, imp)
				if classify(module, imp) != kindInternal || !slices.Contains(cmdTkrAllowed, name) {
					violation(p.ImportPath, imp)
				}
			}
			continue
		}
		name := internalName(module, p.ImportPath)
		if name == "" {
			out = append(out, fmt.Sprintf("%s is not in the A§1.3 table", p.ImportPath))
			continue
		}
		if unchecked[name] {
			continue
		}
		r, ok := allowed[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s is not in the A§1.3 table", p.ImportPath))
			continue
		}
		if name == "cli" && fileImports != nil {
			for _, f := range p.GoFiles {
				extra := cliFileExceptions[f]
				for _, imp := range fileImports(p.Dir, f) {
					if !importAllowed(module, r, extra, imp) {
						violation(p.ImportPath+"/"+f, imp)
					}
				}
			}
			continue
		}
		for _, imp := range p.Imports {
			if !importAllowed(module, r, nil, imp) {
				violation(p.ImportPath, imp)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// parseFileImports returns the import paths of dir/file using go/parser.
func parseFileImports(dir, file string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, file), nil, parser.ImportsOnly)
	if err != nil {
		return []string{"<unparseable " + file + ": " + err.Error() + ">"}
	}
	var imps []string
	for _, s := range f.Imports {
		p, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		imps = append(imps, p)
	}
	return imps
}

func TestCheckerRejectsForbiddenEdges(t *testing.T) {
	got := checkPackages("github.com/linsomniac/taskronin", []listedPackage{
		{ImportPath: "github.com/linsomniac/taskronin/internal/domain", Imports: []string{"time", "github.com/linsomniac/taskronin/internal/store"}},
		{ImportPath: "github.com/linsomniac/taskronin/internal/store", Imports: []string{"github.com/mattn/go-sqlite3"}},
		{ImportPath: "github.com/linsomniac/taskronin/internal/civil", Imports: []string{"time", "database/sql/driver"}},
	}, nil)
	want := []string{
		"github.com/linsomniac/taskronin/internal/domain imports github.com/linsomniac/taskronin/internal/store (not allowed by A§1.3)",
		"github.com/linsomniac/taskronin/internal/store imports github.com/mattn/go-sqlite3 (not allowed by A§1.3)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}

func TestCheckerAcceptsAllowedEdges(t *testing.T) {
	got := checkPackages("github.com/linsomniac/taskronin", []listedPackage{
		{ImportPath: "github.com/linsomniac/taskronin/internal/store", Imports: []string{
			"github.com/linsomniac/taskronin/internal/domain", "modernc.org/sqlite", "modernc.org/sqlite/lib", "database/sql",
		}},
		{ImportPath: "github.com/linsomniac/taskronin/cmd/tkr", Imports: []string{"github.com/linsomniac/taskronin/internal/cli", "os"}},
	}, nil)
	if len(got) != 0 {
		t.Fatalf("unexpected violations: %q", got)
	}
	got = checkPackages("github.com/linsomniac/taskronin", []listedPackage{
		{ImportPath: "github.com/linsomniac/taskronin/cmd/tkr", Imports: []string{"github.com/linsomniac/taskronin/internal/server"}},
	}, nil)
	if len(got) != 1 {
		t.Fatalf("cmd/tkr importing server: violations = %q", got)
	}
}

func TestCheckerCLIFileExceptions(t *testing.T) {
	files := map[string][]string{
		"cmd_add.go":   {"github.com/linsomniac/taskronin/internal/server"},
		"cmd_serve.go": {"github.com/linsomniac/taskronin/internal/server"},
		"cmd_purge.go": {"github.com/linsomniac/taskronin/internal/offline"},
	}
	stub := func(_, file string) []string { return files[file] }
	pkgs := []listedPackage{{
		ImportPath: "github.com/linsomniac/taskronin/internal/cli",
		Dir:        "unused",
		GoFiles:    []string{"cmd_add.go", "cmd_serve.go", "cmd_purge.go"},
	}}
	got := checkPackages("github.com/linsomniac/taskronin", pkgs, stub)
	want := []string{"github.com/linsomniac/taskronin/internal/cli/cmd_add.go imports github.com/linsomniac/taskronin/internal/server (not allowed by A§1.3)"}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}

func TestCheckerUnknownPackage(t *testing.T) {
	got := checkPackages("github.com/linsomniac/taskronin", []listedPackage{
		{ImportPath: "github.com/linsomniac/taskronin/internal/bogus", Imports: []string{"time"}},
	}, nil)
	want := []string{"github.com/linsomniac/taskronin/internal/bogus is not in the A§1.3 table"}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}

func TestDependencyGraph(t *testing.T) {
	modOut, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Path}}\n{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	modLines := strings.Split(strings.TrimSpace(string(modOut)), "\n")
	if len(modLines) != 2 {
		t.Fatalf("unexpected go list -m output %q", modOut)
	}
	module, root := modLines[0], modLines[1]

	cmd := exec.CommandContext(t.Context(), "go", "list", "-json", "./...")
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(stdout)
	for dec.More() {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("go list: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, v := range checkPackages(module, pkgs, parseFileImports) {
		t.Error(v)
	}
}
