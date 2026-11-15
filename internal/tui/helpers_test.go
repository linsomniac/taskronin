package tui

import (
	"bytes"
	"os/exec"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func newTestApp(t *testing.T, f *fakeAPI, opts ...func(*Config)) *App {
	t.Helper()
	cfg := Config{
		API: f, Ctx: t.Context(), Clock: f.clock, PollInterval: -1, Color: false,
		Editor: EditorEnv{DraftsDir: t.TempDir(), Exec: func(_ *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
			if cb == nil {
				return nil
			}
			return func() tea.Msg { return cb(nil) }
		}},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	a := NewApp(cfg)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drain(t, a, a.Init())
	return a
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		r := []rune(s)
		if len(r) != 1 {
			panic("key needs one rune or a supported key name: " + s)
		}
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

func press(t *testing.T, a *App, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, cmd := a.Update(key(k))
		drain(t, a, cmd)
	}
}

func typeText(t *testing.T, a *App, s string) {
	t.Helper()
	for _, r := range s {
		press(t, a, string(r))
	}
}

func drain(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; {
		cmd, queue = queue[0], queue[1:]
		if cmd == nil {
			continue
		}
		n++
		if n > 1000 {
			t.Fatal("drain exceeded 1000 messages")
		}
		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		var msg tea.Msg
		select {
		case msg = <-result:
		case <-timer.C:
			t.Fatal("drain exceeded 2 seconds")
		}
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
			return
		case nil:
			continue
		default:
			_, next := a.Update(msg)
			queue = append(queue, next)
		}
	}
}

func viewText(a *App) string { return a.View().Content }

func contains(s string) func([]byte) bool {
	return func(b []byte) bool { return bytes.Contains(b, []byte(s)) }
}

func ptr[T any](v T) *T { return &v }

type recScreen struct {
	stubScreen
	loads, keys int
}

func (s *recScreen) load(_ *App) tea.Cmd { s.loads++; return nil }

func (s *recScreen) handle(_ *App, _ tea.KeyPressMsg) (tea.Cmd, bool) {
	s.keys++
	return nil, false
}
