package tui

import (
	"strings"

	binding "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"taskronin/internal/textsafe"
)

type keyMap struct {
	Up, Down, Tabs, Add, Log, Search, Undo, Redo, Help, Escape, Quit, ForceQuit, AI, Refresh binding.Binding
}

func newKeys() keyMap {
	bind := func(keys []string, label, desc string) binding.Binding {
		return binding.NewBinding(binding.WithKeys(keys...), binding.WithHelp(label, desc))
	}
	return keyMap{
		Up:        bind([]string{"k", "up"}, "k/↑", "up"),
		Down:      bind([]string{"j", "down"}, "j/↓", "down"),
		Tabs:      bind([]string{"1", "2", "3", "4", "5", "tab", "shift+tab"}, "1–5/tab", "tabs"),
		Add:       bind([]string{"a"}, "a", "add"),
		Log:       bind([]string{"l"}, "l", "log"),
		Search:    bind([]string{"/"}, "/", "search"),
		Undo:      bind([]string{"u"}, "u", "undo"),
		Redo:      bind([]string{"ctrl+r"}, "ctrl+r", "redo"),
		Help:      bind([]string{"?"}, "?", "help"),
		Escape:    bind([]string{"esc"}, "esc", "back/clear"),
		Quit:      bind([]string{"q"}, "q", "quit"),
		ForceQuit: bind([]string{"ctrl+c"}, "ctrl+c", "quit"),
		Refresh:   bind([]string{"r"}, "r", "refresh"),
		AI:        bind([]string{"A"}, "A", "AI changes"),
	}
}

func (k keyMap) exitHint() binding.Binding {
	if k.Quit.Enabled() {
		return k.Quit
	}
	return k.Escape
}

func (k keyMap) ShortHelp() []binding.Binding {
	return []binding.Binding{k.Tabs, k.Add, k.Log, k.Help, k.exitHint()}
}

func (k keyMap) FullHelp() [][]binding.Binding {
	return [][]binding.Binding{
		{k.Up, k.Down, k.Tabs},
		{k.Add, k.Log, k.Search, k.AI},
		{k.Undo, k.Redo},
		{k.Help, k.Escape, k.Quit, k.ForceQuit},
	}
}

// actionKeys is shared by a screen's dispatcher and expanded help. The first
// key is the canonical command; aliases are accepted without changing actions.
type actionKeys []binding.Binding

func action(command, label, description string, aliases ...string) binding.Binding {
	return binding.NewBinding(binding.WithKeys(append([]string{command}, aliases...)...), binding.WithHelp(label, description))
}

func (keys actionKeys) command(k tea.KeyPressMsg) string {
	for _, key := range keys {
		if binding.Matches(k, key) {
			return key.Keys()[0]
		}
	}
	return ""
}

var (
	listKeys        = actionKeys{action("j", "j/↓", "down", "down"), action("k", "k/↑", "up", "up")}
	todayKeys       = appendKeys(listKeys, action("f", "f", "focus"), action("B", "B", "block/reblock"), action("U", "U", "unblock"), action("x", "x", "done"), action("l", "l", "log"), action("esc", "esc", "clear selection"))
	triageKeys      = appendKeys(listKeys, action("enter", "enter", "triage"))
	backlogKeys     = appendKeys(listKeys, action("J", "J", "move down"), action("K", "K", "move up"), action("x", "x", "done"), action("B", "B", "block"), action("d", "d", "defer"), action("f", "f", "focus"), action("e", "e", "edit"), action("D", "D", "drop"), action("enter", "enter", "detail"))
	upcomingKeys    = appendKeys(listKeys, action("K", "K", "keep"), action("d", "d", "defer"), action("enter", "enter", "detail"))
	logKeys         = appendKeys(listKeys, action("l", "l", "log"), action("L", "L", "relink"), action("X", "X", "delete"), action("e", "e", "edit"))
	cardActions     = actionKeys{action("k", "k", "keep"), action("t", "t", "top"), action("b", "b", "bottom"), action("p", "p", "place"), action("d", "d", "defer"), action("B", "B", "block"), action("x", "x", "done"), action("D", "D", "drop"), action("s", "s", "skip"), action("A", "A", "accept new")}
	planBacklogKeys = appendKeys(listKeys, action("B", "B", "block today"), action("enter", "enter", "summary"))
	recapActions    = actionKeys{action("w", "w", "worked"), action("m", "m", "missed"), action("a", "a", "auto"), action("x", "x", "done"), action("l", "l", "log"), action("L", "L", "link")}
	recapKeys       = appendKeys(listKeys, recapActions...)
	detailKeys      = appendKeys(listKeys, action("e", "e", "edit"), action("h", "h", "history"), action("t", "t", "tags"))
	searchKeys      = appendKeys(listKeys, action("r", "r", "refresh"), action("enter", "enter", "detail"))
	aiKeys          = appendKeys(listKeys, action("A", "A", "review/retry"), action("R", "R", "revert"))
)

func appendKeys(keys actionKeys, extra ...binding.Binding) actionKeys {
	return append(append(actionKeys{}, keys...), extra...)
}

func (a *App) screenKeys() actionKeys {
	switch s := a.current().(type) {
	case *todayScreen:
		return todayKeys
	case *triageScreen:
		return triageKeys
	case *backlogScreen:
		return backlogKeys
	case *upcomingScreen:
		return upcomingKeys
	case *logScreen:
		return logKeys
	case *cardMode:
		return cardActions
	case *planMode:
		switch s.step {
		case 1:
			return appendKeys(cardActions, action("enter", "enter", "skip to backlog"))
		case 2:
			return planBacklogKeys
		default:
			return actionKeys{action("enter", "enter", "finish")}
		}
	case *recapMode:
		return recapKeys
	case *detailScreen:
		return detailKeys
	case *historyScreen:
		return listKeys
	case *searchScreen:
		return searchKeys
	case *aiScreen:
		return aiKeys
	}
	return nil
}

func (a *App) shortModeHelp(k keyMap) actionKeys {
	keys := a.screenKeys()
	var deck *cardDeck
	switch s := a.current().(type) {
	case *cardMode:
		deck = s.deck
	case *planMode:
		if s.step == 1 {
			deck = s.deck
		}
	case *recapMode:
		// Leave room for every outcome and linking key on the short line.
		keys = recapActions
	}
	if deck != nil {
		if _, hasCard := deck.current(); hasCard {
			// Both card layouts already render cardLegend in the body.
			return actionKeys{k.Help, k.Quit, k.Escape}
		}
	}
	return appendKeys(keys, k.Help, k.exitHint())
}

func (a *App) helpView(w int) string {
	k := a.keys
	if len(a.overlays) > 0 {
		k.Escape.SetHelp("esc", "back")
		k.Quit.SetEnabled(false)
	} else if a.mode != ModeTabs {
		if !a.exitWithMode {
			k.Quit.SetHelp("q", "back")
		}
		if a.banner.text != "" || len(a.draftPaths) > 0 {
			k.Escape.SetHelp("esc", "clear")
		} else {
			k.Escape.SetHelp("esc", k.Quit.Help().Desc)
		}
	}
	if !a.help.ShowAll {
		if a.mode == ModeTabs {
			return a.help.View(k)
		}
		short := a.help
		short.ShortSeparator = " · "
		return short.ShortHelpView(a.shortModeHelp(k))
	}
	keys := appendKeys(a.screenKeys(), k.Refresh, k.Add, k.Log, k.Search, k.Undo, k.Redo)
	if a.mode == ModeTabs && len(a.overlays) == 0 {
		keys = appendKeys(keys, k.Tabs, k.AI)
	}
	keys = appendKeys(keys, k.Help, k.Escape, k.Quit, k.ForceQuit)
	seen := make(map[string]bool)
	var lines []string
	line := ""
	for _, key := range keys {
		command := key.Keys()[0]
		if !key.Enabled() || seen[command] {
			continue
		}
		seen[command] = true
		h := key.Help()
		item := h.Key + " " + h.Desc
		next := item
		if line != "" {
			next = line + " · " + item
		}
		if textsafe.Width(next) > w && line != "" {
			lines = append(lines, a.styles.Dim.Render(line))
			line = item
		} else {
			line = next
		}
	}
	if line != "" {
		lines = append(lines, a.styles.Dim.Render(line))
	}
	return strings.Join(lines, "\n")
}
