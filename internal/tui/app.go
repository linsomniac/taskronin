package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	binding "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

// DefaultPollInterval is the dashboard refresh interval (A§14 D25).
const DefaultPollInterval = 5 * time.Second

// EditorEnv supplies editor configuration and the process runner.
type EditorEnv struct {
	Setting   string
	Getenv    func(string) string
	DraftsDir string
	Exec      func(*exec.Cmd, tea.ExecCallback) tea.Cmd
}

// Config supplies the app's client, server clock and launch options.
type Config struct {
	API          API
	Ctx          context.Context
	Clock        api.Clock
	StartTab     Tab
	StartMode    Mode
	RecapDate    civil.Date
	PollInterval time.Duration
	Color        bool
	Editor       EditorEnv
}

// App owns the terminal UI. Only Update and its helpers change model state.
type App struct {
	cfg            Config
	api            API
	ctx            context.Context
	width          int
	height         int
	clock          api.Clock
	today          *api.Today
	todayETag      string
	todayLoad      loadState
	pollReq        int
	pollBaseline   *api.Today
	pairedTodayReq int // Today request accompanied by a visible-screen load.
	editorRead     loadState
	tab            Tab
	tabs           [5]screen
	mode           Mode
	modeScreen     screen
	exitWithMode   bool
	overlays       []screen
	overlayDirty   bool // An own operation requires reloading covered overlays on reveal.
	baseDirty      bool // Reload the underlying tab/mode when the last overlay closes.
	form           screen
	banner         banner
	draftPaths     []string
	inflight       bool
	quitting       bool
	reqSeq         map[string]int
	styles         Styles
	keys           keyMap
	help           help.Model
}

var _ tea.Model = (*App)(nil)

// NewApp constructs the app, leaving placeholders for subsequent TUI tasks.
func NewApp(cfg Config) *App {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Editor.Getenv == nil {
		cfg.Editor.Getenv = func(string) string { return "" }
	}
	if cfg.Editor.Exec == nil {
		cfg.Editor.Exec = tea.ExecProcess
	}
	if cfg.RecapDate == (civil.Date{}) {
		cfg.RecapDate = cfg.Clock.Today
	}
	a := &App{
		cfg: cfg, api: cfg.API, ctx: cfg.Ctx, clock: cfg.Clock,
		tab: cfg.StartTab, mode: cfg.StartMode, exitWithMode: cfg.StartMode != ModeTabs,
		reqSeq: make(map[string]int), styles: newStyles(cfg.Color), keys: newKeys(), help: help.New(),
	}
	dim := a.styles.Dim
	a.help.Styles = help.Styles{
		ShortKey: dim, ShortDesc: dim, ShortSeparator: dim,
		FullKey: dim, FullDesc: dim, FullSeparator: dim, Ellipsis: dim,
	}
	for i, name := range tabNames {
		a.tabs[i] = stubScreen{name: name}
	}
	a.tabs[TabToday] = &todayScreen{cursor: -1}
	a.tabs[TabTriage] = &triageScreen{}
	a.tabs[TabBacklog] = &backlogScreen{}
	a.tabs[TabUpcoming] = &upcomingScreen{}
	a.tabs[TabLog] = &logScreen{}
	switch cfg.StartMode {
	case ModeTriage:
		a.modeScreen = newCardMode(0)
	case ModePlan:
		a.modeScreen = newPlanMode()
	case ModeRecap:
		a.modeScreen = newRecapMode(cfg.RecapDate)
	default:
	}
	return a
}

// Init starts the aggregate and visible-screen reads and the polling timer.
func (a *App) Init() tea.Cmd { return tea.Batch(a.refresh(), a.pollTick()) }

// Update handles app messages, then forwards screen-owned data messages.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = max(0, msg.Width), max(0, msg.Height)
		a.help.SetWidth(a.width)
	case tea.KeyPressMsg:
		return a, a.handleKey(msg)
	case pollMsg:
		return a, a.poll()
	case editorTaskMsg:
		if !a.latest("editor", msg.req) {
			// AIDEV-NOTE: this stale read cannot launch an editor; release its
			// reserved mutation guard so later actions are not stranded.
			a.inflight = false
			return a, nil
		}
		cmd := a.editorRead.finish(a, msg.err)
		if msg.err != nil {
			a.inflight = false
			return a, cmd
		}
		d := msg.detail
		return a, a.prepareEditor(editTarget{kind: "task", id: msg.id, etag: d.ETag, origTitle: d.Title, origNotes: d.Notes})
	case editorSetupMsg:
		a.inflight = false
		a.setBanner(bannerError, msg.prefix+msg.err.Error())
		return a, nil
	case editorDoneMsg:
		return a, a.editorDone(msg)
	case draftDiscardedMsg:
		if msg.err != nil {
			a.keepDraft(msg.path)
			a.appendBanner("cannot discard draft: " + msg.err.Error())
		}
		return a, nil
	case todayMsg:
		if msg.poll && a.pollReq == msg.req {
			a.pollReq = 0
		}
		// AIDEV-NOTE: request-sequence staleness guard. Even an old error or
		// 304 must not overwrite state from a newer refresh.
		if !a.latest("today", msg.req) {
			return a, nil
		}
		if msg.err != nil {
			return a, a.todayLoad.finish(a, msg.err)
		}
		a.todayLoad.finish(a, nil)
		var cmds []tea.Cmd
		dayBefore := a.clock.Today
		if !msg.notModified && msg.today != nil {
			a.today, a.todayETag, a.clock = msg.today, msg.etag, msg.today.Clock
			cmds = append(cmds, a.tabs[TabToday].update(a, msg))
		}
		// AIDEV-NOTE: openLog can advance today and its ETag without loading
		// the visible screen. Keep the last dashboard covered by a poll or
		// full refresh, and compare it even when the next poll returns 304.
		if msg.poll || msg.req == a.pairedTodayReq {
			changed := msg.poll && todayPayloadChanged(a.pollBaseline, a.today)
			a.pollBaseline = a.today
			if changed || dayBefore != a.clock.Today {
				if len(a.overlays) > 0 {
					a.baseDirty = true
				} else if a.current() != a.tabs[TabToday] {
					cmds = append(cmds, a.current().load(a))
				}
			}
		}
		// Only accepted, successful reads may refresh a form's implicit link.
		if a.form != nil {
			cmds = append(cmds, a.form.update(a, msg))
			// AIDEV-NOTE: a read-error banner may hide a held retry snapshot. Restore
			// its warning on recovery, including a successful conditional 304.
			if f, ok := a.form.(*form); ok && f.retry != nil && !a.banner.readError {
				f.retryBanner(a)
			}
		}
		return a, tea.Batch(cmds...)
	case opMsg:
		a.inflight = false
		if msg.err != nil {
			var cmd tea.Cmd
			var read *formReadError
			// AIDEV-NOTE: a form's clock/identity preflight is a read even though
			// it runs under the mutation guard. It cannot have saved this attempt.
			if errors.As(msg.err, &read) {
				cmd = a.handleReadErr(read.err)
				a.banner.owner = a.form
			} else {
				cmd = a.handleErr(msg.err)
			}
			if msg.onErr != nil {
				msg.onErr(a, msg.err)
			}
			// Card screens can use a definitive 412's Current after handleErr
			// schedules its refresh; forms retain their own retry/error policy.
			return a, tea.Batch(cmd, a.current().update(a, msg))
		}
		headline := msg.label
		if msg.resp != nil {
			if line := effectsLine(msg.resp.Effects); line != "" {
				headline = line
			}
		}
		a.setBanner(bannerOK, headline)
		var after tea.Cmd
		// The refresh below covers any screen revealed by the success hook.
		a.overlayDirty, a.baseDirty = false, false
		if msg.after != nil {
			after = msg.after(a, msg.resp)
		}
		// A success hook can replace the headline. Keep every response warning with it.
		if msg.resp != nil {
			for _, warning := range msg.resp.Warnings {
				a.appendBanner("⚠ " + warning)
			}
		}
		a.overlayDirty = len(a.overlays) > 0
		return a, tea.Batch(a.refresh(), after)
	}

	var cmds []tea.Cmd
	for _, s := range a.tabs {
		cmds = append(cmds, s.update(a, msg))
	}
	if a.modeScreen != nil {
		cmds = append(cmds, a.modeScreen.update(a, msg))
	}
	for _, s := range a.overlays {
		cmds = append(cmds, s.update(a, msg))
	}
	if a.form != nil {
		cmds = append(cmds, a.form.update(a, msg))
	}
	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if binding.Matches(k, a.keys.ForceQuit) {
		return a.quit()
	}
	if a.form != nil {
		cmd, _ := a.form.handle(a, k)
		return cmd
	}
	if binding.Matches(k, a.keys.Quit) && a.inflight {
		a.setBanner(bannerInfo, "busy… (ctrl+c forces)")
		return nil
	}
	if binding.Matches(k, a.keys.Escape) && len(a.draftPaths) > 0 {
		a.draftPaths, a.banner = nil, banner{}
		return nil
	}
	if len(a.overlays) > 0 {
		if binding.Matches(k, a.keys.Escape) {
			return a.closeOverlay()
		}
	} else if a.mode != ModeTabs && binding.Matches(k, a.keys.Quit, a.keys.Escape) {
		if binding.Matches(k, a.keys.Escape) && a.banner.text != "" {
			a.banner = banner{}
			return nil
		}
		return a.exitMode()
	}
	// AIDEV-NOTE: arrow aliases apply only to lists in tabs/overlays. In card
	// modes k keeps a task, so rewriting up to k could mutate data. Forms
	// receive their original keys above.
	if a.mode == ModeTabs || len(a.overlays) > 0 {
		switch k.String() {
		case "up":
			k = tea.KeyPressMsg{Code: 'k', Text: "k"}
		case "down":
			k = tea.KeyPressMsg{Code: 'j', Text: "j"}
		}
	}
	if cmd, consumed := a.current().handle(a, k); consumed {
		return cmd
	}
	if binding.Matches(k, a.keys.Refresh) {
		return a.refresh()
	}
	if binding.Matches(k, a.keys.Help) {
		a.help.ShowAll = !a.help.ShowAll
		return nil
	}
	if binding.Matches(k, a.keys.Escape) {
		a.banner = banner{}
		return nil
	}
	if binding.Matches(k, a.keys.Add) {
		return a.openAdd()
	}
	if binding.Matches(k, a.keys.Log) {
		return a.openLog(a.clock.Today, a.logLink())
	}
	if binding.Matches(k, a.keys.Search) {
		return a.openSearch()
	}
	if binding.Matches(k, a.keys.Undo) {
		return a.mutate("undone", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.Undo(ctx, client.Mut{})
		}, nil, nil)
	}
	if binding.Matches(k, a.keys.Redo) {
		return a.mutate("redone", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.Redo(ctx, client.Mut{})
		}, nil, nil)
	}
	if a.mode == ModeTabs && len(a.overlays) == 0 && binding.Matches(k, a.keys.AI) {
		return a.openOverlay(newAIOverlay())
	}
	if a.mode != ModeTabs || len(a.overlays) > 0 {
		return nil
	}
	previousTab := a.tab
	switch s := k.String(); s {
	case "1", "2", "3", "4", "5":
		a.tab = Tab(s[0] - '1')
	case "tab":
		a.tab = (a.tab + 1) % Tab(len(a.tabs))
	case "shift+tab":
		a.tab = (a.tab + Tab(len(a.tabs)) - 1) % Tab(len(a.tabs))
	case "q":
		return a.quit()
	default:
		return nil
	}
	if previousTab != a.tab && a.pollReq != 0 && a.latest("today", a.pollReq) {
		// The new tab has its own load. Discard an older poll, including its
		// ETag, so the next tick can fetch the current aggregate.
		a.nextReq("today")
	}
	return a.current().load(a)
}

func (a *App) quit() tea.Cmd { a.quitting = true; return tea.Quit }

// View renders a bounded alternate-screen frame, with prompts above the footer.
func (a *App) View() tea.View {
	w, h := a.width, a.height
	footer := a.helpView(w)
	if a.banner.text != "" {
		style, prefix := a.styles.Info, ""
		switch a.banner.kind {
		case bannerInfo:
		case bannerOK:
			style, prefix = a.styles.OK, "✓ "
		case bannerError:
			style, prefix = a.styles.Err, "⚠ "
		}
		notice := style.Render(a.line(prefix+a.banner.text, w))
		if a.help.ShowAll {
			footer = notice + "\n" + footer
		} else {
			footer = notice
		}
	}
	bottom := append(a.draftLines(w), strings.Split(footer, "\n")...)
	formHeight := 0
	if a.form != nil {
		formHeight = 3
	}
	lines := strings.Split(a.header(w), "\n")
	bodyHeight := max(0, h-len(lines)-len(bottom)-formHeight)
	lines = append(lines, fitLines(a.current().view(a, w, bodyHeight), bodyHeight)...)
	if a.form != nil {
		lines = append(lines, fitLines(a.form.view(a, w, formHeight), formHeight)...)
	}
	lines = append(lines, bottom...)
	v := tea.NewView(clip(lines, w, h))
	v.AltScreen = true
	return v
}

func (a *App) current() screen {
	if n := len(a.overlays); n > 0 {
		return a.overlays[n-1]
	}
	if a.modeScreen != nil {
		return a.modeScreen
	}
	return a.tabs[a.tab]
}

// AIDEV-NOTE: allocate request IDs while creating commands, never inside their
// closures. Creation order defines latest even when commands execute out of order.
func (a *App) nextReq(target string) int          { a.reqSeq[target]++; return a.reqSeq[target] }
func (a *App) latest(target string, req int) bool { return a.reqSeq[target] == req }

type todayMsg struct {
	req         int
	poll        bool
	today       *api.Today
	etag        string
	notModified bool
	err         error
}

func (a *App) fetchToday(poll bool) tea.Cmd {
	req, ctx, c := a.nextReq("today"), a.ctx, a.api
	etag := ""
	if !poll {
		a.todayLoad = loadLoading
	}
	if poll {
		etag = a.todayETag
		a.pollReq = req
	}
	return func() tea.Msg {
		td, tag, notModified, err := c.Today(ctx, etag)
		return todayMsg{req: req, poll: poll, today: td, etag: tag, notModified: notModified, err: err}
	}
}

func (a *App) refresh() tea.Cmd {
	a.baseDirty = len(a.overlays) > 0
	today := a.fetchToday(false)
	a.pairedTodayReq = a.reqSeq["today"]
	return tea.Batch(today, a.current().load(a))
}

type opMsg struct {
	label string
	resp  *api.OpResponse
	err   error
	after func(*App, *api.OpResponse) tea.Cmd
	onErr func(*App, error)
}

func (a *App) mutate(label string, call func(context.Context, API) (*api.OpResponse, error),
	after func(*App, *api.OpResponse) tea.Cmd, onErr func(*App, error),
) tea.Cmd {
	// AIDEV-NOTE: in-flight guard is set before scheduling the command, so
	// repeated keys cannot send another mutation while this one is pending.
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil
	}
	a.inflight = true
	ctx, c := a.ctx, a.api
	return func() tea.Msg {
		resp, err := call(ctx, c)
		return opMsg{label: label, resp: resp, err: err, after: after, onErr: onErr}
	}
}

func (a *App) setTabScreen(t Tab, s screen) { a.tabs[t] = s }

func (a *App) enterMode(m Mode, s screen) tea.Cmd {
	a.mode, a.modeScreen = m, s
	return s.load(a)
}

func (a *App) exitMode() tea.Cmd {
	a.clearScreenBanner(a.modeScreen)
	if a.exitWithMode {
		return a.quit()
	}
	a.mode, a.modeScreen = ModeTabs, nil
	return a.current().load(a)
}

func (a *App) openOverlay(s screen) tea.Cmd {
	a.overlays = append(a.overlays, s)
	return s.load(a)
}

func (a *App) closeOverlay() tea.Cmd {
	if n := len(a.overlays); n > 0 {
		a.clearScreenBanner(a.overlays[n-1])
		a.overlays[n-1] = nil
		a.overlays = a.overlays[:n-1]
		// AIDEV-NOTE: own ops refresh the visible overlay immediately. Coalesce
		// the hidden tab/mode's reload until the last overlay is closed; polls
		// deliberately do not reload overlays (CF02). Error/explicit refreshes
		// and changed poll payloads also mark that underlying screen dirty.
		// Only own operations also invalidate other overlays in the stack.
		if len(a.overlays) == 0 {
			a.overlayDirty = false
			if a.baseDirty {
				a.baseDirty = false
				return a.current().load(a)
			}
		} else if a.overlayDirty {
			return a.current().load(a)
		}
	}
	return nil
}
