package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"lazymermaid/internal/config"
	"lazymermaid/internal/document"
	"lazymermaid/internal/graphics"
	"lazymermaid/internal/render"
)

func testModel(t *testing.T) *Model {
	t.Helper()
	c := config.Default()
	c.Preview.Mode = "unicode"
	m := NewModel(context.Background(), c, t.TempDir(), nil, graphics.New(&bytes.Buffer{}))
	d, e := document.Parse("example.md", []byte("# First\n```mermaid\nflowchart LR\n A-->B\n```\n# Second\n```mermaid\nsequenceDiagram\n A->>B: hi\n```\n"))
	if e != nil {
		t.Fatal(e)
	}
	m.diagrams = d.Diagrams
	m.refilter()
	return m
}
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}
func TestInputOwnsNavigationAndQuit(t *testing.T) {
	m := testModel(t)
	m.Update(key("/"))
	for _, r := range "jkhql/?專案" {
		m.Update(key(string(r)))
	}
	if !m.filtering || m.query != "jkhql/?專案" || m.closed || m.overlay != "" {
		t.Fatalf("input routed to action: %+v", m)
	}
	m.Update(key("esc"))
	if m.query != "" || m.filtering {
		t.Fatal("escape did not clear")
	}
}
func TestLateRenderCannotOverwriteCurrentPreview(t *testing.T) {
	m := testModel(t)
	m.revision = 9
	m.previewText = "new"
	m.Update(renderMsg{revision: 8, err: errors.New("old failure")})
	if m.previewText != "new" || strings.Contains(m.status, "old failure") {
		t.Fatal("stale failure applied")
	}
	m.Update(renderMsg{revision: 8, result: render.Result{Data: []byte("old success"), Format: "unicode"}})
	if m.previewText != "new" {
		t.Fatal("stale success applied")
	}
}
func TestRefreshDoesNotFollowChangedOrdinal(t *testing.T) {
	m := testModel(t)
	old := m.diagrams[0]
	m.scanGeneration = 2
	d, e := document.Parse("example.md", []byte("# Added\n```mermaid\npie\n \"a\": 2\n```\n# First\n```mermaid\nflowchart LR\n A-->B\n```\n"))
	if e != nil {
		t.Fatal(e)
	}
	m.Update(scanMsg{generation: 2, diagrams: d.Diagrams})
	selected, ok := m.selected()
	if !ok || selected.Source != old.Source || selected.ID == old.ID {
		t.Fatalf("selection followed ordinal: %+v", selected)
	}
	d, e = document.Parse("example.md", []byte("# Replacement\n```mermaid\npie\n \"a\": 3\n```\n"))
	if e != nil {
		t.Fatal(e)
	}
	m.Update(scanMsg{generation: 2, diagrams: d.Diagrams})
	if _, ok = m.selected(); ok {
		t.Fatal("ambiguous replacement auto-selected")
	}
}
func TestLayoutClampsAndContainsUnicode(t *testing.T) {
	m := testModel(t)
	m.status = "專案 é 👩🏽‍💻"
	for _, size := range [][2]int{{0, 0}, {20, 8}, {80, 24}, {150, 45}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := m.View()
		if strings.Contains(v.Content, "panic") {
			t.Fatal(v.Content)
		}
		if size[0] >= 80 && !strings.Contains(ansi.Strip(v.Content), "專案 é 👩🏽‍💻") {
			t.Fatal("graphemes lost")
		}
		if size[0] >= 80 {
			for _, line := range strings.Split(v.Content, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("row exceeds width %d: %q", size[0], line)
				}
			}
		}
	}
}
func TestDiagnosticsDetailsAndExamples(t *testing.T) {
	m := testModel(t)
	m.diagnosticText = "Parse error\nLine 4: unexpected token"
	m.Update(key("d"))
	if !strings.Contains(m.View().Content, "unexpected token") {
		t.Fatal("error details unavailable")
	}
	source := firstExample("# Example\n```mermaid-example\nflowchart LR\n A-->B\n```\n")
	if source != "flowchart LR\n A-->B\n" {
		t.Fatal(source)
	}
}

func TestScanPreservesUnsavedRowsAndInvalidatesMissingTarget(t *testing.T) {
	m := testModel(t)
	m.diagrams[0].Source = "flowchart LR\n A-->Unsaved\n"
	m.diagrams[0].DocumentHash = "editor-version"
	m.snapshot.Path = m.diagrams[0].Path
	m.snapshot.Dirty = true
	m.scanGeneration = 1
	disk, _ := document.Parse("example.md", []byte("# Different\n```mermaid\npie\n \"a\": 1\n```\n"))
	m.Update(scanMsg{generation: 1, diagrams: disk.Diagrams})
	d, ok := m.selected()
	if !ok || !strings.Contains(d.Source, "Unsaved") {
		t.Fatal("scan replaced dirty diagram")
	}
	m.snapshot.Dirty = false
	m.revision = 10
	m.pending = true
	cancelled := false
	m.cancelRender = func() { cancelled = true }
	m.Update(scanMsg{generation: 1, diagrams: disk.Diagrams})
	if m.revision <= 10 || m.pending || !m.stale || !cancelled {
		t.Fatal("invalidated source retained an active render")
	}
}
func TestClosedHandbookCannotReopenOnLateRender(t *testing.T) {
	m := testModel(t)
	m.openDocs()
	g := m.docGeneration
	m.overlay = ""
	m.Update(docMsg{generation: g, body: "late handbook"})
	if m.overlay != "" {
		t.Fatal("late document reopened an overlay")
	}
}
