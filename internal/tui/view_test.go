package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"lazymermaid/internal/document"
	"lazymermaid/internal/editor"
)

func listModel(t *testing.T, count int) *Model {
	t.Helper()
	m := testModel(t)
	m.loading = false
	m.width, m.height = 100, 20
	m.diagrams = make([]document.Diagram, count)
	for i := range m.diagrams {
		m.diagrams[i] = document.Diagram{ID: fmt.Sprintf("doc#%d", i), Path: "guide.md", Title: fmt.Sprintf("Diagram %02d", i), StartLine: i + 1}
	}
	m.refilter()
	return m
}

func TestListHitMatchesScrolledVisibleRows(t *testing.T) {
	m := listModel(t, 40)
	m.selection = 35
	r := m.layout().list
	body := r.content()
	rows := m.visibleListRows(r)
	if rows.start == 0 {
		t.Fatal("fixture must scroll away from the first row")
	}
	rendered := strings.Split(ansi.Strip(m.listView(r)), "\n")
	for index := rows.start; index < rows.end; index++ {
		y := body.Y + rows.headers + index - rows.start
		got, ok := m.listHit(body.X, y)
		if !ok || got != index {
			t.Fatalf("visible row %d hit %d, %v", index, got, ok)
		}
		if line := rendered[y-r.y]; !strings.Contains(line, m.diagrams[m.filtered[index]].Title) {
			t.Fatalf("hit row differs from rendered row: index %d, line %q", index, line)
		}
	}
}

func TestListHitExcludesBordersTitlesAndFilter(t *testing.T) {
	for _, active := range []bool{false, true} {
		m := listModel(t, 40)
		m.selection = 39
		m.query = "diagram"
		m.filtering = active
		r := m.layout().list
		body := r.content()
		rows := m.visibleListRows(r)
		if rows.headers != 1 {
			t.Fatal("both active and retained filters occupy one row")
		}
		for _, point := range [][2]int{
			{r.x, body.Y + 1},           // left border
			{r.x + r.w - 1, body.Y + 1}, // right border
			{body.X, r.y},               // top border
			{body.X, r.y + 1},           // title
			{body.X, r.y + r.h - 1},     // bottom border
			{body.X, body.Y},            // filter field/retained query
			{body.X, r.y + r.h},         // outside the pane
			{r.x + r.w, body.Y + 1},     // next pane
		} {
			if index, ok := m.listHit(point[0], point[1]); ok {
				t.Errorf("non-row %v selected %d", point, index)
			}
		}
		if got, ok := m.listHit(body.X, body.Y+1); !ok || got != rows.start {
			t.Errorf("first row after filter: %d, %v; want %d", got, ok, rows.start)
		}
	}
}

func TestListHitRejectsEmptyAndUnusedRows(t *testing.T) {
	m := listModel(t, 1)
	body := m.layout().list.content()
	if index, ok := m.listHit(body.X, body.Y); !ok || index != 0 {
		t.Fatalf("the single visible diagram was not selectable: %d, %v", index, ok)
	}
	if _, ok := m.listHit(body.X, body.Y+1); ok {
		t.Fatal("empty space below a row was actionable")
	}
	m.filtered = nil
	if _, ok := m.listHit(body.X, body.Y); ok {
		t.Fatal("No diagrams placeholder was actionable")
	}
	m.width, m.height, m.focus = 40, 10, 1
	if _, ok := m.listHit(1, 4); ok {
		t.Fatal("hidden list in narrow editor layout was actionable")
	}
	m.focus, m.height = 0, 5
	m.filtered = []int{0}
	if _, ok := m.listHit(1, 3); ok {
		t.Fatal("list without body space was actionable")
	}
}

func TestListHitReturnsFilteredSelectionIndex(t *testing.T) {
	m := listModel(t, 10)
	m.filtered = []int{2, 5, 9}
	m.selection = 1
	body := m.layout().list.content()
	if index, ok := m.listHit(body.X, body.Y+1); !ok || index != 1 {
		t.Fatalf("wanted filtered index 1, got %d, %v", index, ok)
	}
}

func TestSourceTitleFollowsTheActualEditorBuffer(t *testing.T) {
	m := testModel(t)
	// Leave the sidebar on example.md; active snapshots below intentionally
	// belong to other buffers, as happens with :buffer or a scratch diagram.
	for _, tt := range []struct {
		name string
		snap editor.Snapshot
		want string
	}{
		{"original", editor.Snapshot{Buffer: 3, Path: "/project/actual.md", CursorLine: 12}, " actual.md:12"},
		{"dirty original", editor.Snapshot{Buffer: 3, Path: "/project/actual.md", CursorLine: 12, Dirty: true}, " actual.md:12 [+]"},
		{"scratch", editor.Snapshot{Buffer: 4, Source: "flowchart TD\n", Dirty: true}, " New diagram [+]"},
		{"clean scratch", editor.Snapshot{Buffer: 4}, " New diagram"},
		{"virtual", editor.Snapshot{Buffer: 5, Path: "lazymermaid://block-2/diagram.mmd", ParentPath: "/project/parent.md", BodyStartLine: 27, Virtual: true}, " parent.md:27 · Mermaid-only"},
		{"virtual without parent metadata", editor.Snapshot{Buffer: 5, Virtual: true}, " Mermaid block · Mermaid-only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m.snapshot = tt.snap
			if got := m.sourceTitle(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	m.snapshot = editor.Snapshot{}
	if got := m.sourceTitle(); !strings.Contains(got, "example.md") {
		t.Fatalf("read-only view lost selected source fallback: %q", got)
	}
}
