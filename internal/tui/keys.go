package tui

import binding "charm.land/bubbles/v2/key"

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
		Quit:      bind([]string{"q"}, "q", "quit/back"),
		ForceQuit: bind([]string{"ctrl+c"}, "ctrl+c", "quit"),
		Refresh:   bind([]string{"r"}, "r", "refresh"),
		AI:        bind([]string{"A"}, "A", "AI changes"),
	}
}

func (k keyMap) ShortHelp() []binding.Binding {
	return []binding.Binding{k.Tabs, k.Add, k.Log, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]binding.Binding {
	return [][]binding.Binding{
		{k.Up, k.Down, k.Tabs},
		{k.Add, k.Log, k.Search, k.AI},
		{k.Undo, k.Redo},
		{k.Help, k.Escape, k.Quit, k.ForceQuit},
	}
}
