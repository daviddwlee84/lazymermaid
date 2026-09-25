package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"lazymermaid/internal/graphics"
)

type rect struct{ x, y, w, h int }

func (r rect) content() graphics.Rect {
	return graphics.Rect{X: r.x + 1, Y: r.y + 2, Width: max(0, r.w-2), Height: max(0, r.h-3)}
}
func (r rect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type layout struct {
	list, editor, preview rect
	kind                  int
}

func (m *Model) layout() layout {
	w, h := m.width, max(3, m.height-3)
	if w < 75 || h < 12 {
		l := layout{kind: 2}
		r := rect{0, 1, w, h}
		switch m.focus {
		case 0:
			l.list = r
		case 1:
			l.editor = r
		case 2:
			l.preview = r
		}
		return l
	}
	lw := min(28, w/4)
	if w >= 120 {
		return layout{list: rect{0, 1, lw, h}, editor: rect{lw, 1, (w - lw) / 2, h}, preview: rect{lw + (w-lw)/2, 1, w - lw - (w-lw)/2, h}}
	}
	return layout{list: rect{0, 1, lw, h}, editor: rect{lw, 1, w - lw, h / 2}, preview: rect{lw, 1 + h/2, w - lw, h - h/2}, kind: 1}
}
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}
func fit(s string, w, h int) string {
	if h <= 0 {
		return ""
	}
	ls := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		if i < len(ls) {
			out[i] = clip(ls[i], w)
		}
		out[i] += strings.Repeat(" ", max(0, w-ansi.StringWidth(out[i])))
	}
	return strings.Join(out, "\n")
}
func box(r rect, title, body string, focused bool) string {
	if r.w < 2 || r.h < 3 {
		return fit(body, max(1, r.w), max(1, r.h))
	}
	c := lipgloss.Color("#49566b")
	if focused {
		c = accent
	}
	inner := clip(title, r.w-2) + "\n" + fit(body, r.w-2, r.h-3)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).Render(inner)
}
func (m *Model) View() tea.View {
	if m.width < 8 || m.height < 5 {
		v := tea.NewView("Resize terminal")
		v.AltScreen = true
		return v
	}
	title := fmt.Sprintf(" lazymermaid  %s  ·  %s / %s", label(filepath.Base(m.root)), m.mode, m.theme)
	if m.snapshot.Dirty {
		title += "  ● unsaved"
	}
	if m.pending {
		title += "  rendering…"
	} else if m.stale {
		title += "  stale preview"
	}
	var body string
	var cursor *tea.Cursor
	l := m.layout()
	if m.overlay != "" {
		body = m.overlayView()
	} else {
		list := m.listView(l.list)
		source, sourceCursor := m.sourceView(l.editor)
		preview := m.previewView(l.preview)
		switch l.kind {
		case 0:
			body = lipgloss.JoinHorizontal(lipgloss.Top, list, source, preview)
		case 1:
			body = lipgloss.JoinHorizontal(lipgloss.Top, list, lipgloss.JoinVertical(lipgloss.Left, source, preview))
		case 2:
			switch m.focus {
			case 0:
				body = list
			case 1:
				body = source
			case 2:
				body = preview
			}
		}
		if sourceCursor != nil && m.focus == 1 && !m.filtering {
			c := *sourceCursor
			r := l.editor.content()
			c.X += r.X
			c.Y += r.Y
			cursor = &c
		}
	}
	footer := m.footer()
	status := label(m.status)
	if d, ok := m.selected(); ok {
		path := d.Path
		if rel, err := filepath.Rel(m.root, d.Path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		status = fmt.Sprintf("%s:%d-%d · %s", label(path), d.StartLine, d.EndLine, status)
	}
	v := tea.NewView(fit(title, m.width, 1) + "\n" + fit(body, m.width, max(1, m.height-3)) + "\n" + fit(status, m.width, 1) + "\n" + fit(footer, m.width, 1))
	v.AltScreen = true
	v.ReportFocus = true
	v.Cursor = cursor
	v.WindowTitle = "lazymermaid"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
func (m *Model) listView(r rect) string {
	if r.w == 0 {
		return ""
	}
	title := fmt.Sprintf(" Diagrams %d/%d", len(m.filtered), len(m.diagrams))
	var lines []string
	if m.filtering {
		lines = append(lines, "/ "+m.input.View())
	} else if m.query != "" {
		lines = append(lines, "/ "+label(m.query))
	}
	if m.loading && len(m.diagrams) == 0 {
		lines = append(lines, "Scanning…")
	}
	if !m.loading && len(m.filtered) == 0 {
		lines = append(lines, "No diagrams", "n new · F1 handbook")
	}
	rows := m.visibleListRows(r)
	for j := rows.start; j < rows.end; j++ {
		d := m.diagrams[m.filtered[j]]
		mark := "  "
		if j == m.selection {
			mark = "› "
		}
		name := d.Title
		if name == "" {
			name = filepath.Base(d.Path)
		}
		s := mark + label(name) + fmt.Sprintf(" :%d", d.StartLine)
		if j == m.selection {
			s = lipgloss.NewStyle().Foreground(accent).Bold(true).Render(clip(s, r.w-2))
		}
		lines = append(lines, s)
	}
	return box(r, title, strings.Join(lines, "\n"), m.focus == 0)
}

// listRows describes the same filtered-row viewport used for drawing and clicks.
// Header rows are inside the box body; its border and title are separate.
type listRows struct {
	start, end, headers int
}

func (m *Model) visibleListRows(r rect) listRows {
	rows := listRows{}
	if m.filtering || m.query != "" {
		rows.headers = 1
	}
	available := max(0, r.h-3-rows.headers)
	if r.w <= 2 || available == 0 {
		return rows
	}
	rows.start = min(len(m.filtered), max(0, m.selection-available+1))
	rows.end = min(len(m.filtered), rows.start+available)
	return rows
}

// listHit returns an index in m.filtered (the selection index), never an index
// into m.diagrams. Only a visible diagram row is actionable.
func (m *Model) listHit(x, y int) (int, bool) {
	r := m.layout().list
	body := r.content()
	if body.Width <= 0 || body.Height <= 0 || x < body.X || x >= body.X+body.Width || y < body.Y || y >= body.Y+body.Height {
		return 0, false
	}
	rows := m.visibleListRows(r)
	index := rows.start + y - body.Y - rows.headers
	if index < rows.start || index >= rows.end {
		return 0, false
	}
	return index, true
}

func (m *Model) sourceTitle() string {
	title := " Source"
	snapshot := m.snapshot
	switch {
	case snapshot.Virtual:
		title = " Mermaid block"
		if snapshot.ParentPath != "" {
			title = " " + label(filepath.Base(snapshot.ParentPath))
			if snapshot.BodyStartLine > 0 {
				title += fmt.Sprintf(":%d", snapshot.BodyStartLine)
			}
		}
		title += " · Mermaid-only"
	case snapshot.Path != "":
		title = " " + label(filepath.Base(snapshot.Path))
		if snapshot.CursorLine > 0 {
			title += fmt.Sprintf(":%d", snapshot.CursorLine)
		}
	case snapshot.Buffer != 0:
		title = " New diagram"
	default:
		if d, ok := m.selected(); ok {
			title = " " + label(filepath.Base(d.Path)) + fmt.Sprintf(":%d", d.StartLine)
		}
	}
	if snapshot.Dirty {
		title += " [+]"
	}
	return title
}

func (m *Model) sourceView(r rect) (string, *tea.Cursor) {
	if r.w == 0 {
		return "", nil
	}
	title := m.sourceTitle()
	if m.pane != nil {
		v := m.pane.View()
		return box(r, title, v.Content, m.focus == 1), v.Cursor
	}
	body := safe(m.source)
	if body == "" {
		body = "Select a diagram or press n to create one.\n\nNeovim enables in-pane editing.\nRun lazymermaid doctor for optional tools."
	}
	if m.editorStarting {
		body = "Starting Neovim…\n\n" + body
	}
	return box(r, title, body, m.focus == 1), nil
}
func (m *Model) previewView(r rect) string {
	if r.w == 0 {
		return ""
	}
	title := " Preview · " + m.mode
	if m.pending {
		title += " · pending"
	} else if m.stale {
		title += " · stale"
	}
	var body string
	if m.mode == "image" && m.lastResult.Format == "png" {
		body = ""
	} else if m.previewText != "" {
		ls := strings.Split(m.previewText, "\n")
		start := min(m.previewScroll, max(0, len(ls)-1))
		for i := start; i < len(ls); i++ {
			ls[i] = ansi.Cut(ls[i], m.previewHorizontal, m.previewHorizontal+max(1, r.w-2))
		}
		body = strings.Join(ls[start:], "\n")
	} else {
		body = "Select a diagram to preview.\n\nImage: optional official Mermaid runtime\nText: optional termaid\n\nt switches mode · x exports\nlazymermaid doctor shows setup"
		if m.diagnosticText != "" {
			body = "Preview unavailable · d details\n\n" + safe(m.diagnosticText)
		}
	}
	return box(r, title, body, m.focus == 2)
}
func (m *Model) overlayView() string {
	r := rect{0, 1, m.width, max(3, m.height-3)}
	var body, title string
	switch m.overlay {
	case "diagnostics":
		title = " Renderer / syntax diagnostics"
		body = m.diagnosticText
		if body == "" {
			body = "No current errors. Official validation requires the Mermaid runtime."
		}
		lines := strings.Split(safe(body), "\n")
		body = strings.Join(lines[min(m.overlayScroll, max(0, len(lines)-1)):], "\n")
	case "help":
		title = " Keyboard help"
		lines := strings.Split(helpText(), "\n")
		body = strings.Join(lines[min(m.overlayScroll, max(0, len(lines)-1)):], "\n")
	case "quit":
		title = " Unsaved changes"
		body = "These buffers have unsaved changes:\n"
		for _, d := range m.dirty {
			body += "  " + label(d.Path) + "\n"
		}
		body += "\nEsc return to Neovim (:wa saves all)\nd discard changes and quit"
	case "export":
		title = " Export"
		body = "Output path (.svg, .png, .txt, .unicode, .ascii):\n\n" + m.input.View() + "\n\nEnter export · Esc cancel\nExisting files are not overwritten."
	case "docs":
		title = " Official Mermaid handbook"
		body = "Search: " + m.input.View() + "\n\n"
		available := max(1, m.height-9)
		offset := max(0, m.hitSelection-available+1)
		for i := offset; i < len(m.hits) && i < offset+available; i++ {
			h := m.hits[i]
			mark := "  "
			if i == m.hitSelection {
				mark = "› "
			}
			body += mark + h.Title + "\n"
		}
		body += "\n↑↓ choose · Enter read · Esc return"
	case "doc":
		title = " " + m.overlayTitle
		lines := strings.Split(m.overlayBody, "\n")
		start := min(m.overlayScroll, max(0, len(lines)-1))
		body = strings.Join(lines[start:], "\n")
	}
	return box(r, title, body, true)
}
func (m *Model) footer() string {
	if m.overlay == "doc" {
		return " ↑↓/jk scroll · PgUp/PgDn · n use example · Esc search handbook"
	}
	if m.overlay != "" {
		return " Esc back · Ctrl-G switches panes outside overlays"
	}
	if m.filtering {
		return " Type to filter · ↑↓ select · Enter accept · Esc clear"
	}
	if m.focus == 1 {
		return " Neovim · :w save · Ctrl-G panes · " + hints("docs")
	}
	if m.focus == 2 {
		return " hjkl pan · " + hints("mode", "fit", "export", "help", "quit")
	}
	if len(m.diagrams) == 0 {
		return " " + hints("new", "docs", "help", "quit")
	}
	return " ↑↓/jk select · " + hints("edit", "filter", "mode", "help", "quit")
}
