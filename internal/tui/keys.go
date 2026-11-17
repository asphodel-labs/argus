package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// Binding is one key binding: the keys that start it and the help text.
type Binding struct {
	Keys []string
	Desc string
}

// Matches reports whether the key press starts the binding.
func (b Binding) Matches(msg tea.KeyPressMsg) bool {
	return slices.Contains(b.Keys, msg.String())
}

// KeyMap holds the key bindings. Global bindings work in both panes. The
// help overlay shows the global bindings and the bindings of the focused
// pane.
type KeyMap struct {
	Quit        Binding
	ForceQuit   Binding
	Help        Binding
	Refresh     Binding
	FocusSwitch Binding
	FocusList   Binding
	FocusDetail Binding
	Close       Binding

	List    []Binding
	Detail  []Binding
	Actions []Binding
}

// DefaultKeyMap returns the key bindings of the plan. The list pane, the
// detail pane, and the dialogs implement the pane and action bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:        Binding{Keys: []string{"q"}, Desc: "quit"},
		ForceQuit:   Binding{Keys: []string{"ctrl+c"}, Desc: "quit"},
		Help:        Binding{Keys: []string{"?"}, Desc: "help"},
		Refresh:     Binding{Keys: []string{"ctrl+r"}, Desc: "refresh now"},
		FocusSwitch: Binding{Keys: []string{"tab"}, Desc: "switch pane"},
		FocusList:   Binding{Keys: []string{"h", "left"}, Desc: "focus list"},
		FocusDetail: Binding{Keys: []string{"l", "right"}, Desc: "focus detail"},
		Close:       Binding{Keys: []string{"esc"}, Desc: "close dialog"},
		List: []Binding{
			{Keys: []string{"j", "k", "down", "up"}, Desc: "down / up"},
			{Keys: []string{"gg", "G", "home", "end"}, Desc: "top / bottom"},
			{Keys: []string{"ctrl+d", "ctrl+u"}, Desc: "half page down / up"},
			{Keys: []string{"pgdown", "pgup"}, Desc: "page down / up"},
			{Keys: []string{"]", "["}, Desc: "next / previous section"},
			{Keys: []string{"enter"}, Desc: "open PR, focus it"},
			{Keys: []string{"space"}, Desc: "open PR, stay in list"},
			{Keys: []string{"za"}, Desc: "fold section or repo"},
			{Keys: []string{"/"}, Desc: "find PR"},
			{Keys: []string{"a"}, Desc: "hide approved"},
			{Keys: []string{"d"}, Desc: "hide drafts"},
			{Keys: []string{"b"}, Desc: "bots: all / only / none"},
			{Keys: []string{"u"}, Desc: "author filter"},
			{Keys: []string{"m"}, Desc: "load more"},
		},
		Detail: []Binding{
			{Keys: []string{"j", "k", "down", "up"}, Desc: "scroll down / up"},
			{Keys: []string{"gg", "G", "home", "end"}, Desc: "top / bottom"},
			{Keys: []string{"ctrl+d", "ctrl+u"}, Desc: "half page down / up"},
			{Keys: []string{"pgdown", "pgup"}, Desc: "page down / up"},
			{Keys: []string{"]", "["}, Desc: "next / previous thread"},
			{Keys: []string{"za"}, Desc: "fold thread"},
		},
		Actions: []Binding{
			{Keys: []string{"A"}, Desc: "approve"},
			{Keys: []string{"M"}, Desc: "merge"},
			{Keys: []string{"U"}, Desc: "update branch"},
			{Keys: []string{"R"}, Desc: "request review"},
			{Keys: []string{"@"}, Desc: "assign"},
			{Keys: []string{"f", "F"}, Desc: "rerun failed / all"},
			{Keys: []string{"c"}, Desc: "comment"},
			{Keys: []string{"r"}, Desc: "reply"},
			{Keys: []string{"+"}, Desc: "react"},
			{Keys: []string{"x"}, Desc: "resolve thread"},
			{Keys: []string{"o"}, Desc: "open in browser"},
			{Keys: []string{"y"}, Desc: "copy URL"},
			{Keys: []string{":"}, Desc: "command line"},
			{Keys: []string{"p"}, Desc: "Claude /pr session"},
			{Keys: []string{"v"}, Desc: "review with revdiff"},
		},
	}
}

// HelpSection is one titled group of bindings in the help overlay.
type HelpSection struct {
	Title    string
	Bindings []Binding
}

// HelpFor returns the help sections for the focused pane.
func (k KeyMap) HelpFor(f Focus) []HelpSection {
	global := HelpSection{Title: "Global", Bindings: []Binding{
		k.Help, k.Refresh, k.FocusSwitch, k.FocusList, k.FocusDetail, k.Close, k.Quit,
	}}
	actions := HelpSection{Title: "PR actions", Bindings: k.Actions}
	if f == FocusList {
		return []HelpSection{{Title: "List pane", Bindings: k.List}, actions, global}
	}
	return []HelpSection{{Title: "Detail pane", Bindings: k.Detail}, actions, global}
}
