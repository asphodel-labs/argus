package tui

import tea "charm.land/bubbletea/v2"

// wheelLines is the number of lines that one wheel step scrolls.
const wheelLines = 3

// handleMouse handles a click or a wheel step on the panes. The mouse does
// nothing while a dialog or the help overlay is open.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if len(m.dialogs) > 0 || m.help {
		return nil
	}
	mouse := msg.Mouse()
	listWidth, height := m.layout()
	if mouse.Y < 0 || mouse.Y >= height {
		return nil
	}
	pane := FocusList
	if mouse.X >= listWidth {
		pane = FocusDetail
	}
	// The top border takes the first line of a pane.
	row := mouse.Y - 1
	switch msg.(type) {
	case tea.MouseWheelMsg:
		n := wheelLines
		switch mouse.Button {
		case tea.MouseWheelUp:
			n = -n
		case tea.MouseWheelDown:
		default:
			return nil
		}
		if pane == FocusList {
			m.list.scrollBy(n)
		} else {
			m.detail.layout()
			m.detail.offset += n
			m.detail.clamp()
		}
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return nil
		}
		m.focus = pane
		if pane == FocusList {
			return m.clickList(row)
		}
		m.detail.click(row)
	}
	return nil
}

// scrollBy moves the list view n rows and keeps the cursor in the view.
func (l *listPane) scrollBy(n int) {
	h := l.viewHeight()
	l.offset = max(min(l.offset+n, len(l.rows)-h), 0)
	l.cursor = max(min(l.cursor, l.offset+h-1, len(l.rows)-1), l.offset, 0)
}

// clickList moves the cursor to the clicked row and acts on it: it opens a
// PR, folds a section or a repository group, or loads more PRs.
func (m *Model) clickList(row int) tea.Cmd {
	l := &m.list
	i := l.offset + row
	if row < 0 || row >= l.viewHeight() || i >= len(l.rows) {
		return nil
	}
	l.setCursor(i)
	switch l.rows[i].kind {
	case rowPR:
		return m.openPR(l.rows[i].pr)
	case rowSection, rowGroup:
		l.toggleFold()
	case rowFooter:
		return m.loadMore()
	}
	return nil
}

// click makes the clicked item current and folds or opens it. A click on a
// line that is not an item header does nothing.
func (d *detailPane) click(row int) {
	if !d.hasSel || row < 0 || row >= d.height {
		return
	}
	d.layout()
	line := d.offset + row
	for i, it := range d.items {
		if it.line == line {
			d.cur = i
			d.toggleFold()
			return
		}
	}
}
