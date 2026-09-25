// Package tui contains the host UI. Domain work is shared with the CLI and runs
// in commands; the Bubble Tea update loop owns selection and render freshness.
package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymermaid/internal/config"
	"github.com/daviddwlee84/lazymermaid/internal/document"
	"github.com/daviddwlee84/lazymermaid/internal/editor"
	"github.com/daviddwlee84/lazymermaid/internal/graphics"
	"github.com/daviddwlee84/lazymermaid/internal/handbook"
	"github.com/daviddwlee84/lazymermaid/internal/render"
)

type scanMsg struct {
	diagrams   []document.Diagram
	issues     []document.ScanIssue
	err        error
	generation int
}
type editorReady struct {
	pane *editor.Pane
	err  error
}
type operationMsg struct {
	status string
	err    error
}
type debounceMsg struct{ revision int }
type renderMsg struct {
	revision      int
	result        render.Result
	validation    []render.Diagnostic
	validationErr error
	err           error
	snapshot      editor.Snapshot
	ctx           context.Context
}
type editorIntent struct {
	kind    string
	diagram document.Diagram
	source  string
}
type editorOpened struct {
	generation int
	snapshot   editor.Snapshot
	err        error
	pane       *editor.Pane
}
type editorClosed struct{ err error }
type validationMsg struct {
	revision int
	result   render.Result
	err      error
	snapshot editor.Snapshot
}
type quitMsg struct {
	dirty []editor.DirtyBuffer
	err   error
}
type docMsg struct {
	body, title string
	raw         string
	err         error
	generation  int
}
type capabilityMsg struct{ image bool }
type watchMsg struct{}
type Model struct {
	ctx                              context.Context
	cfg                              config.Config
	root                             string
	service                          *render.Service
	presenter                        *graphics.Presenter
	width, height, focus             int
	diagrams                         []document.Diagram
	filtered                         []int
	selection                        int
	query                            string
	input                            textinput.Model
	filtering                        bool
	overlay                          string
	overlayBody, overlayTitle        string
	overlayRaw                       string
	diagnosticText                   string
	modeTouched                      bool
	overlayScroll                    int
	hits                             []handbook.Hit
	hitSelection                     int
	docGeneration                    int
	exportFormat                     string
	status                           string
	loading                          bool
	scanGeneration                   int
	pane                             *editor.Pane
	editorStarting                   bool
	revision                         int
	pending                          bool
	source                           string
	snapshot                         editor.Snapshot
	lastResult                       render.Result
	previewText                      string
	previewScroll, previewHorizontal int
	stale                            bool
	mode                             string
	theme                            string
	gPending                         bool
	dirty                            []editor.DirtyBuffer
	cancelRender                     context.CancelFunc
	watch                            <-chan struct{}
	closed                           bool
	openBusy                         bool
	openGeneration                   int
	openDesired                      editorIntent
	queuedEditor                     []tea.Msg
	quitChecking                     bool
	forceQuit                        bool
	replayingSnapshot                bool
}

func Run(ctx context.Context, cfg config.Config, path, color string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err = os.Stat(abs); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	svc := render.New(render.Options{Node: cfg.Render.Node, RuntimeDir: cfg.Render.RuntimeDir, Termaid: cfg.Render.Termaid, CacheDir: config.CacheDir(), Timeout: cfg.Timeout()})
	defer svc.Close()
	presenter := graphics.New(os.Stdout)
	defer presenter.Close()
	m := NewModel(runCtx, cfg, abs, svc, presenter)
	m.watch = startWatch(runCtx, abs)
	opts := []tea.ProgramOption{tea.WithContext(runCtx), tea.WithOutput(presenter)}
	if color == "never" || (color == "auto" && os.Getenv("NO_COLOR") != "") {
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	if color == "always" {
		opts = append(opts, tea.WithColorProfile(colorprofile.TrueColor))
	}
	p := tea.NewProgram(m, opts...)
	_, err = p.Run()
	if m.pane != nil {
		closeErr := m.pane.Close(m.forceQuit || runCtx.Err() != nil || err != nil)
		if err == nil {
			err = closeErr
		}
	}
	return err
}
func NewModel(ctx context.Context, cfg config.Config, path string, svc *render.Service, p *graphics.Presenter) *Model {
	in := textinput.New()
	in.Prompt = ""
	in.SetVirtualCursor(true)
	mode := cfg.Preview.Mode
	if mode == "auto" {
		mode = "unicode"
	}
	return &Model{ctx: ctx, cfg: cfg, root: path, service: svc, presenter: p, width: 100, height: 30, input: in, mode: mode, theme: cfg.Preview.Theme, status: "Discovering diagrams…", loading: true}
}
func (m *Model) Init() tea.Cmd {
	runtimeDir := m.cfg.Render.RuntimeDir
	return tea.Batch(m.scan(), m.startEditor(), m.waitWatch(), func() tea.Msg {
		_, err := os.Stat(filepath.Join(runtimeDir, "node_modules", "mermaid", "package.json"))
		return capabilityMsg{err == nil && graphics.Available()}
	})
}
func (m *Model) scan() tea.Cmd {
	m.scanGeneration++
	g := m.scanGeneration
	ctx, path, cfg := m.ctx, m.root, m.cfg
	return func() tea.Msg {
		d, i, e := document.Scan(ctx, path, document.ScanOptions{Hidden: cfg.Scan.Hidden, NoIgnore: cfg.Scan.NoIgnore})
		return scanMsg{d, i, e, g}
	}
}
func (m *Model) startEditor() tea.Cmd {
	if m.editorStarting || m.pane != nil {
		return nil
	}
	m.editorStarting = true
	r := m.layout().editor.content()
	cfg, ctx, dir := m.cfg, m.ctx, m.root
	if st, e := os.Stat(dir); e == nil && !st.IsDir() {
		dir = filepath.Dir(dir)
	}
	return func() tea.Msg {
		p, e := editor.New(ctx, editor.Options{Width: max(1, r.Width), Height: max(1, r.Height), Directory: dir, NvimPath: cfg.Editor.Command, RuntimePaths: cfg.Editor.RuntimePaths})
		return editorReady{p, e}
	}
}
func (m *Model) waitWatch() tea.Cmd {
	if m.watch == nil {
		return nil
	}
	ch, ctx := m.watch, m.ctx
	return func() tea.Msg {
		select {
		case <-ch:
			return watchMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *Model) selected() (document.Diagram, bool) {
	if m.selection < 0 || m.selection >= len(m.filtered) {
		return document.Diagram{}, false
	}
	i := m.filtered[m.selection]
	if i >= len(m.diagrams) {
		return document.Diagram{}, false
	}
	return m.diagrams[i], true
}
func (m *Model) refilter() {
	m.filtered = nil
	q := strings.ToLower(m.query)
	for i, d := range m.diagrams {
		if q == "" || strings.Contains(strings.ToLower(d.Title+" "+d.Path+" "+d.Source), q) {
			m.filtered = append(m.filtered, i)
		}
	}
	m.selection = max(0, min(m.selection, len(m.filtered)-1))
}
func (m *Model) selectDiagram() tea.Cmd {
	d, ok := m.selected()
	if !ok {
		return nil
	}
	m.source = d.Source
	m.snapshot = editor.Snapshot{}
	m.previewScroll = 0
	m.previewHorizontal = 0
	cmds := []tea.Cmd{m.schedule()}
	if m.pane != nil {
		cmds = append(cmds, m.requestEditor(editorIntent{kind: "file", diagram: d}))
	}
	return tea.Batch(cmds...)
}
func (m *Model) schedule() tea.Cmd {
	m.revision++
	m.pending = true
	m.stale = len(m.lastResult.Data) > 0
	if m.cancelRender != nil {
		m.cancelRender()
		m.cancelRender = nil
	}
	r := m.revision
	return tea.Tick(time.Duration(m.cfg.Render.DebounceMS)*time.Millisecond, func(time.Time) tea.Msg { return debounceMsg{r} })
}
func (m *Model) renderLatest() tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelRender = cancel
	rev, source, mode, theme, snap, svc, conf := m.revision, m.source, m.mode, m.theme, m.snapshot, m.service, m.cfg.Render.MermaidConfig
	return func() tea.Msg {
		format := mode
		if format == "image" {
			format = "png"
		}
		r, e := svc.Render(ctx, render.Request{Source: source, Format: format, Theme: theme, Config: json.RawMessage(conf)})
		return renderMsg{revision: rev, result: r, err: e, snapshot: snap, ctx: ctx}
	}
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.syncPresentation()
	switch v := msg.(type) {
	case capabilityMsg:
		if m.cfg.Preview.Mode == "auto" && !m.modeTouched && v.image {
			m.mode = "image"
			if m.source != "" {
				return m, m.schedule()
			}
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		m.input.SetWidth(max(1, m.width-8))
		if m.pane != nil {
			r := m.layout().editor.content()
			return m, m.pane.Resize(max(1, r.Width), max(1, r.Height))
		}
		return m, nil
	case scanMsg:
		if v.generation != m.scanGeneration {
			return m, nil
		}
		m.loading = false
		if v.err != nil {
			m.status = "Scan failed: " + v.err.Error()
			return m, nil
		}
		old, had := m.selected()
		incoming := v.diagrams
		if m.snapshot.Dirty && !m.snapshot.Virtual && m.snapshot.Path != "" {
			kept := make([]document.Diagram, 0, len(incoming))
			for _, d := range incoming {
				if !samePath(d.Path, m.snapshot.Path) {
					kept = append(kept, d)
				}
			}
			for _, d := range m.diagrams {
				if samePath(d.Path, m.snapshot.Path) {
					kept = append(kept, d)
				}
			}
			incoming = kept
		}
		m.diagrams = incoming
		m.sortDiagrams()
		m.refilter()
		matched := !had
		if had {
			m.selection = -1
			match := -1
			for j, i := range m.filtered {
				d := m.diagrams[i]
				if d.ID == old.ID && d.DocumentHash == old.DocumentHash {
					match = j
					break
				}
				if d.Path == old.Path && d.Source == old.Source && d.Title == old.Title {
					if match >= 0 {
						match = -2
						break
					}
					match = j
				}
			}
			if match >= 0 {
				m.selection = match
				matched = true
			}
		}
		m.status = fmt.Sprintf("%d diagrams", len(m.diagrams))
		if len(v.issues) > 0 {
			m.status += fmt.Sprintf(" · %d scan warnings: %s", len(v.issues), v.issues[0].Message)
		}
		if m.snapshot.Dirty {
			if !had {
				m.selection = -1
			}
			return m, nil
		}
		if !matched {
			m.status = "Source changed; select the diagram again"
			m.invalidatePreview()
			return m, nil
		}
		return m, m.selectDiagram()
	case watchMsg:
		return m, tea.Batch(m.scan(), m.waitWatch())
	case editorReady:
		m.editorStarting = false
		if v.err != nil {
			m.status = "Read-only source · " + v.err.Error() + " · use doctor"
			return m, nil
		}
		m.pane = v.pane
		r := m.layout().editor.content()
		return m, tea.Batch(m.pane.Init(), m.pane.Resize(max(1, r.Width), max(1, r.Height)), m.waitEditorEvent(), m.selectDiagram())
	case editor.Event:
		if v.Kind == "changed" && m.pane == nil {
			return m, nil
		}
		if v.Kind == "changed" && m.openBusy {
			return m, m.waitEditorEvent()
		}
		if v.Err != nil {
			m.status = v.Err.Error()
		}
		if v.Kind == "exit" {
			m.openBusy = false
			m.openGeneration++
			m.queuedEditor = nil
			m.pane = nil
			m.status = "Neovim exited · e reopen editor"
			return m, nil
		}
		if v.Kind == "changed" {
			previousSnapshot := m.snapshot
			m.snapshot = v.Snapshot
			if v.Snapshot.Virtual {
				m.source = v.Snapshot.Source
				m.selection = -1
				for j, i := range m.filtered {
					d := m.diagrams[i]
					if samePath(d.Path, v.Snapshot.ParentPath) && d.BodyStartLine == v.Snapshot.BodyStartLine {
						m.selection = j
						break
					}
				}
				return m, tea.Batch(m.schedule(), m.waitEditorEvent())
			}
			if v.Snapshot.Path == "" {
				m.selection = -1
				m.source = v.Snapshot.Source
				return m, tea.Batch(m.schedule(), m.waitEditorEvent())
			}
			previous, had := m.selected()
			parsed, err := document.Parse(v.Snapshot.Path, []byte(v.Snapshot.Source))
			if err == nil {
				var current *document.Diagram
				for i := range parsed.Diagrams {
					candidate := &parsed.Diagrams[i]
					if v.Snapshot.CursorLine >= candidate.StartLine && v.Snapshot.CursorLine <= candidate.EndLine {
						current = candidate
						break
					}
				}
				if current == nil && had && samePath(previous.Path, v.Snapshot.Path) {
					for i := range parsed.Diagrams {
						candidate := &parsed.Diagrams[i]
						if candidate.Source == previous.Source && candidate.Title == previous.Title {
							if current != nil {
								current = nil
								break
							}
							current = candidate
						}
					}
				}
				updated := make([]document.Diagram, 0, len(m.diagrams)+len(parsed.Diagrams))
				for _, existing := range m.diagrams {
					if !samePath(existing.Path, v.Snapshot.Path) {
						updated = append(updated, existing)
					}
				}
				updated = append(updated, parsed.Diagrams...)
				m.diagrams = updated
				m.sortDiagrams()
				m.refilter()
				if current != nil {
					for j, i := range m.filtered {
						if m.diagrams[i].ID == current.ID {
							m.selection = j
							break
						}
					}
					changed := m.source != current.Source || previousSnapshot.Buffer != v.Snapshot.Buffer || previousSnapshot.ChangedTick != v.Snapshot.ChangedTick
					m.source = current.Source
					if changed || m.lastResult.Data == nil {
						return m, tea.Batch(m.schedule(), m.waitEditorEvent())
					}
				}
				if current == nil {
					m.status = "Cursor is outside a Mermaid block; select a diagram to preview"
					m.stale = true
					m.revision++
					m.pending = false
				}
			}

		}
		if m.pane != nil {
			return m, m.waitEditorEvent()
		}
		return m, nil
	case debounceMsg:
		if v.revision != m.revision {
			return m, nil
		}
		return m, m.renderLatest()
	case renderMsg:
		if v.revision != m.revision {
			return m, nil
		}
		m.pending = false
		m.diagnosticText = ""
		if v.err != nil {
			m.status = "Preview: " + v.err.Error()
			m.diagnosticText = v.err.Error()
			m.stale = true
		} else {
			m.lastResult = v.result
			m.stale = false
			m.status = "Preview ready · " + m.mode
			if v.result.Format == "png" {
				if e := m.presenter.SetPNG(v.result.Data); e != nil {
					m.status = e.Error()
				}
			} else {
				m.previewText = safe(string(v.result.Data))
			}
		}
		diagnostics := append(v.result.Diagnostics, v.validation...)
		if v.validationErr != nil {
			m.status = "Validation: " + v.validationErr.Error()
			m.diagnosticText += "\n\nOfficial Mermaid: " + v.validationErr.Error()
		}
		if m.diagnosticText != "" {
			m.status = "Render/validation error · d details · " + m.status
		}
		cmd := m.applyDiagnostics(v.snapshot, diagnostics)
		if m.mode == "unicode" || m.mode == "ascii" {
			rev, src, snap, svc, ctx := m.revision, m.source, v.snapshot, m.service, v.ctx
			if ctx == nil {
				ctx = m.ctx
			}
			return m, tea.Batch(cmd, func() tea.Msg { r, e := svc.Validate(ctx, src); return validationMsg{rev, r, e, snap} })
		}
		return m, cmd
	case validationMsg:
		if v.revision != m.revision {
			return m, nil
		}
		if render.IsUnavailable(v.err) {
			m.status += " · official validation unavailable"
			return m, nil
		}
		if v.err != nil {
			m.diagnosticText += "\n\nOfficial Mermaid: " + v.err.Error()
			m.status = "Official Mermaid error · d details"
		}
		return m, m.applyDiagnostics(v.snapshot, v.result.Diagnostics)
	case operationMsg:
		if v.err != nil {
			m.status = v.err.Error()
		} else if v.status != "" {
			m.status = v.status
		}
		return m, nil
	case editorOpened:
		if v.pane != m.pane {
			return m, nil
		}
		m.openBusy = false
		if v.generation != m.openGeneration {
			return m, m.beginEditorOpen()
		}
		if v.err != nil {
			m.status = v.err.Error()
			m.queuedEditor = nil
			return m, nil
		}
		// All earlier Open requests have finished; this snapshot is authoritative.
		m.replayingSnapshot = true
		_, cmd := m.Update(editor.Event{Kind: "changed", Snapshot: v.snapshot})
		m.replayingSnapshot = false
		commands := []tea.Cmd{cmd}
		if m.pane != nil {
			for _, input := range m.queuedEditor {
				commands = append(commands, m.pane.Update(input))
			}
		}
		m.queuedEditor = nil
		return m, tea.Sequence(commands...)
	case quitMsg:
		if v.err != nil {
			m.quitChecking = false
			m.status = v.err.Error()
			return m, nil
		}
		if len(v.dirty) > 0 {
			m.quitChecking = false
			m.dirty = v.dirty
			m.overlay = "quit"
			return m, nil
		}
		if m.pane == nil {
			m.closed = true
			return m, tea.Quit
		}
		p := m.pane
		return m, func() tea.Msg { return editorClosed{p.Close(false)} }
	case editorClosed:
		if v.err != nil {
			m.quitChecking = false
			m.status = v.err.Error()
			return m, nil
		}
		m.closed = true
		return m, tea.Quit
	case docMsg:
		if v.generation != m.docGeneration || m.overlay != "docs" {
			return m, nil
		}
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		m.overlay = "doc"
		m.overlayBody = v.body
		m.overlayTitle = v.title
		m.overlayRaw = v.raw
		m.overlayScroll = 0
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(v)
	case tea.PasteMsg:
		if m.overlay == "docs" || m.overlay == "export" || m.filtering {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(v)
			m.queryChanged()
			return m, cmd
		}
		if m.focus == 1 && m.pane != nil {
			if m.quitChecking {
				return m, nil
			}
			if m.openBusy {
				m.queuedEditor = append(m.queuedEditor, v)
				return m, nil
			}
			return m, m.pane.Update(v)
		}
		return m, nil
	case tea.MouseClickMsg:
		if m.overlay != "" {
			return m, nil
		}
		l := m.layout()
		for i, r := range []rect{l.list, l.editor, l.preview} {
			if r.contains(v.X, v.Y) {
				m.focus = i
				if i == 0 {
					if index, ok := m.listHit(v.X, v.Y); ok {
						m.selection = index
						return m, m.selectDiagram()
					}
					return m, nil
				}
				if i == 1 && m.pane != nil {
					c := r.content()
					if v.X < c.X || v.X >= c.X+c.Width || v.Y < c.Y || v.Y >= c.Y+c.Height {
						return m, nil
					}
					v.X -= c.X
					v.Y -= c.Y
					return m, m.pane.Update(v)
				}
			}
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.overlay != "" {
			if v.Button == tea.MouseWheelUp {
				m.overlayScroll = max(0, m.overlayScroll-3)
			} else {
				m.overlayScroll += 3
			}
			return m, nil
		}
		if m.focus == 1 && m.pane != nil {
			r := m.layout().editor.content()
			v.X -= r.X
			v.Y -= r.Y
			return m, m.pane.Update(v)
		}
		if v.Button == tea.MouseWheelUp {
			m.previewScroll = max(0, m.previewScroll-3)
		} else {
			m.previewScroll += 3
		}
		return m, nil
	}
	if m.pane != nil {
		return m, m.pane.Update(msg)
	}
	return m, nil
}
func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if m.quitChecking || m.closed {
		return nil
	}
	key := k.String()
	if m.overlay != "" {
		return m.overlayKey(k)
	}
	if key == "ctrl+g" {
		m.focus = (m.focus + 1) % 3
		return nil
	}
	if key == "f1" {
		return m.openDocs()
	}
	if m.filtering {
		switch key {
		case "esc":
			m.filtering = false
			m.query = ""
			m.input.Blur()
			m.refilter()
			return nil
		case "enter":
			m.filtering = false
			m.input.Blur()
			return m.selectDiagram()
		case "up":
			m.selection = max(0, m.selection-1)
			return nil
		case "down":
			m.selection = min(len(m.filtered)-1, m.selection+1)
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		m.queryChanged()
		return cmd
	}
	if m.focus == 1 && m.pane != nil {
		if m.openBusy {
			m.queuedEditor = append(m.queuedEditor, k)
			return nil
		}
		return m.pane.Update(k)
	}
	key = canonicalHostKey(key)
	switch key {
	case "q", "ctrl+c":
		return m.requestQuit()
	case "tab":
		m.focus = (m.focus + 1) % 3
		return nil
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
		return nil
	case "?":
		m.overlay = "help"
		m.overlayScroll = 0
		return nil
	case "/":
		m.filtering = true
		m.input.SetValue(m.query)
		return m.input.Focus()
	case "e":
		m.focus = 1
		if m.pane == nil {
			return m.startEditor()
		}
		return m.selectDiagram()
	case "v":
		if d, ok := m.selected(); ok && m.pane != nil {
			m.focus = 1
			return m.requestEditor(editorIntent{kind: "virtual", diagram: d})
		}
		return nil
	case "n":
		if m.pane == nil {
			m.status = "Neovim is required to create a diagram · e starts it"
			return nil
		}
		m.focus = 1
		m.selection = -1
		return m.requestEditor(editorIntent{kind: "scratch", source: "flowchart LR\n    A --> B\n"})
	case "t":
		m.modeTouched = true
		m.mode = map[string]string{"image": "unicode", "unicode": "ascii", "ascii": "image"}[m.mode]
		return m.schedule()
	case "T":
		m.theme = map[string]string{"dark": "default", "default": "forest", "forest": "neutral", "neutral": "dark"}[m.theme]
		if m.theme == "" {
			m.theme = "dark"
		}
		return m.schedule()
	case "r":
		return m.schedule()
	case "R":
		m.loading = true
		return m.scan()
	case "d":
		m.overlay = "diagnostics"
		m.overlayScroll = 0
		return nil
	case "x":
		if m.openBusy {
			m.status = "Wait for the source to open before exporting"
			return nil
		}
		m.overlay = "export"
		m.exportFormat = "svg"
		if m.mode == "ascii" || m.mode == "unicode" {
			m.exportFormat = m.mode
		}
		name := "diagram." + extension(m.exportFormat)
		origin, line := "", 1
		if m.snapshot.Buffer != 0 {
			origin = m.snapshot.Path
			line = max(1, m.snapshot.CursorLine)
			if m.snapshot.Virtual {
				origin = m.snapshot.ParentPath
				line = m.snapshot.BodyStartLine
			}
		}
		if d, ok := m.selected(); ok && (m.snapshot.Buffer == 0 || samePath(d.Path, origin)) {
			origin = d.Path
			line = d.StartLine
		}
		if origin != "" {
			name = fmt.Sprintf("%s-L%d.%s", strings.TrimSuffix(filepath.Base(origin), filepath.Ext(origin)), line, extension(m.exportFormat))
		}
		m.input.SetValue(name)
		return m.input.Focus()
	case "y":
		if m.mode == "image" {
			m.status = "Use x to export SVG/PNG"
			return nil
		}
		m.status = "Copied preview via OSC 52"
		return tea.Raw("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(m.previewText)) + "\a")
	case "+", "=":
		m.presenter.Zoom(1.25)
		return nil
	case "-":
		m.presenter.Zoom(.8)
		return nil
	case "f":
		m.presenter.Fit()
		m.previewScroll = 0
		m.previewHorizontal = 0
		return nil
	}
	if m.focus == 2 {
		switch key {
		case "j", "down":
			m.previewScroll++
			m.presenter.Pan(0, 1)
		case "k", "up":
			m.previewScroll = max(0, m.previewScroll-1)
			m.presenter.Pan(0, -1)
		case "h", "left":
			m.previewHorizontal = max(0, m.previewHorizontal-2)
			m.presenter.Pan(-1, 0)
		case "l", "right":
			m.previewHorizontal += 2
			m.presenter.Pan(1, 0)
		}
		return nil
	}
	switch key {
	case "j", "down":
		m.selection = min(len(m.filtered)-1, m.selection+1)
		m.gPending = false
		return m.selectDiagram()
	case "k", "up":
		m.selection = max(0, m.selection-1)
		m.gPending = false
		return m.selectDiagram()
	case "home":
		m.selection = 0
		return m.selectDiagram()
	case "G", "end":
		m.selection = max(0, len(m.filtered)-1)
		return m.selectDiagram()
	case "g":
		if m.gPending {
			m.selection = 0
			m.gPending = false
			return m.selectDiagram()
		}
		m.gPending = true
	case "enter":
		m.focus = 1
		return m.selectDiagram()
	case "h", "left":
		m.focus = 0
	case "l", "right":
		m.focus = 2
	default:
		m.gPending = false
	}
	return nil
}
func (m *Model) queryChanged() {
	if m.filtering && m.query != m.input.Value() {
		m.query = m.input.Value()
		m.selection = 0
		m.refilter()
	}
	if m.overlay == "docs" {
		m.hits = handbook.Search(m.input.Value(), 100)
		m.hitSelection = 0
	}
}
func (m *Model) requestQuit() tea.Cmd {
	if m.openBusy || len(m.queuedEditor) > 0 {
		m.status = "Opening source; wait for the editor before quitting"
		return nil
	}
	m.quitChecking = true
	if m.pane == nil {
		m.closed = true
		return tea.Quit
	}
	p := m.pane
	return func() tea.Msg { d, e := p.DirtyBuffers(); return quitMsg{d, e} }
}
func (m *Model) openDocs() tea.Cmd {
	m.docGeneration++
	m.overlay = "docs"
	m.overlayScroll = 0
	m.input.SetValue("")
	m.hits = handbook.Search("", 100)
	m.hitSelection = 0
	return m.input.Focus()
}
func (m *Model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "esc" {
		if m.overlay == "doc" {
			return m.openDocs()
		}
		m.overlay = ""
		m.docGeneration++
		m.input.Blur()
		return nil
	}
	switch m.overlay {
	case "quit":
		if key == "d" {
			m.forceQuit = true
			m.closed = true
			return tea.Quit
		}
		return nil
	case "help":
		if key == "?" || key == "q" {
			m.overlay = ""
		}
		if key == "j" || key == "down" {
			m.overlayScroll++
		}
		if key == "k" || key == "up" {
			m.overlayScroll = max(0, m.overlayScroll-1)
		}
		return nil
	case "diagnostics":
		if key == "j" || key == "down" {
			m.overlayScroll++
		}
		if key == "k" || key == "up" {
			m.overlayScroll = max(0, m.overlayScroll-1)
		}
		return nil
	case "doc":
		switch key {
		case "j", "down":
			m.overlayScroll++
		case "k", "up":
			m.overlayScroll = max(0, m.overlayScroll-1)
		case "pgdown", "space":
			m.overlayScroll += max(1, m.height-8)
		case "pgup":
			m.overlayScroll = max(0, m.overlayScroll-max(1, m.height-8))
		case "n":
			if m.pane != nil {
				example := firstExample(m.overlayRaw)
				if example != "" {
					m.overlay = ""
					m.focus = 1
					m.selection = -1
					return m.requestEditor(editorIntent{kind: "scratch", source: example})
				}
			}
		}
		return nil
	case "docs":
		switch key {
		case "up":
			m.hitSelection = max(0, m.hitSelection-1)
			return nil
		case "down":
			m.hitSelection = min(len(m.hits)-1, m.hitSelection+1)
			return nil
		case "enter":
			if m.hitSelection >= 0 && m.hitSelection < len(m.hits) {
				m.docGeneration++
				generation := m.docGeneration
				slug := m.hits[m.hitSelection].Slug
				w := max(20, m.width-8)
				return func() tea.Msg {
					p, b, e := handbook.Read(slug)
					if e != nil {
						return docMsg{err: e, generation: generation}
					}
					rendered := b
					if r, e := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(w)); e == nil {
						if s, e := r.Render(b); e == nil {
							rendered = s
						}
					}
					return docMsg{body: rendered, raw: b, title: p.Title + " · Mermaid " + p.Version + " · " + p.Source, generation: generation}
				}
			}
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		m.queryChanged()
		return cmd
	case "export":
		if key == "enter" {
			out := m.input.Value()
			if out == "" {
				return nil
			}
			f := strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), ".")
			if f == "txt" {
				f = m.mode
				if f == "image" {
					f = "unicode"
				}
			}
			if f != "svg" && f != "png" && f != "unicode" && f != "ascii" {
				m.status = "Use .svg, .png, .unicode, .ascii or .txt"
				return nil
			}
			source, theme, svc := m.source, m.theme, m.service
			conf := m.cfg.Render.MermaidConfig
			ctx := m.ctx
			m.overlay = ""
			m.input.Blur()
			return func() tea.Msg {
				if _, e := os.Stat(out); e == nil {
					return operationMsg{err: fmt.Errorf("%s already exists; choose another export path", out)}
				}
				r, e := svc.Render(ctx, render.Request{Source: source, Format: f, Theme: theme, Config: json.RawMessage(conf)})
				if e != nil {
					return operationMsg{err: e}
				}
				file, e := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
				if e != nil {
					return operationMsg{err: e}
				}
				_, e = file.Write(r.Data)
				closeErr := file.Close()
				if e == nil {
					e = closeErr
				}
				return operationMsg{status: "Exported " + out, err: e}
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return cmd
	}
	return nil
}
func (m *Model) syncPresentation() {
	if m.pane != nil {
		if m.focus == 1 && m.overlay == "" && !m.filtering {
			m.pane.Focus()
		} else {
			m.pane.Blur()
		}
	}
	l := m.layout()
	m.presenter.Configure(l.preview.content(), !m.closed && m.width >= 8 && m.height >= 5 && m.overlay == "" && m.mode == "image" && m.lastResult.Format == "png" && l.preview.w > 0)
}
func samePath(a, b string) bool { aa, _ := filepath.Abs(a); bb, _ := filepath.Abs(b); return aa == bb }
func extension(f string) string {
	if f == "ascii" || f == "unicode" {
		return "txt"
	}
	return f
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, ansi.Strip(s))
}
func label(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(safe(s), "\n", " "), "\t", " ")
}
func firstExample(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	var result []string
	active := false
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		if !active {
			if strings.HasPrefix(trim, "```mermaid") {
				active = true
			}
			continue
		}
		if strings.HasPrefix(trim, "```") {
			return strings.Join(result, "\n") + "\n"
		}
		result = append(result, l)
	}
	return ""
}

var _ tea.Model = (*Model)(nil)
var accent = lipgloss.Color("#7dcfff")

func (m *Model) applyDiagnostics(snap editor.Snapshot, diagnostics []render.Diagnostic) tea.Cmd {
	if m.pane == nil || snap.Buffer == 0 {
		return nil
	}
	p := m.pane
	start := 1
	if !snap.Virtual {
		if block, ok := m.selected(); ok {
			start = max(1, block.BodyStartLine)
		}
	}
	ds := make([]editor.Diagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		line, col, message := start, 1, d.Message
		if d.Reliable && d.Line > 0 {
			line = start + d.Line - 1
			col = max(1, d.Column)
		} else {
			message = "Mermaid block (upstream location unavailable): " + message
		}
		ds = append(ds, editor.Diagnostic{Message: message, Line: line, Column: col, Source: "Mermaid"})
	}
	return func() tea.Msg { return operationMsg{err: p.SetDiagnostics(snap, ds)} }
}

// At most one navigation RPC runs at once. While it runs, newer selections
// replace the desired target; intermediate targets never start another RPC.
func (m *Model) requestEditor(intent editorIntent) tea.Cmd {
	m.openGeneration++
	m.openDesired = intent
	return m.beginEditorOpen()
}
func (m *Model) beginEditorOpen() tea.Cmd {
	if m.openBusy || m.pane == nil {
		return nil
	}
	m.openBusy = true
	generation, intent, p := m.openGeneration, m.openDesired, m.pane
	return func() tea.Msg {
		var err error
		switch intent.kind {
		case "file":
			err = p.Open(intent.diagram.Path, max(1, intent.diagram.BodyStartLine), 1)
		case "virtual":
			err = p.OpenVirtual(intent.diagram)
		case "scratch":
			err = p.OpenScratch(intent.source)
		}
		var snapshot editor.Snapshot
		if err == nil {
			snapshot, err = p.CurrentSnapshot()
		}
		return editorOpened{generation: generation, snapshot: snapshot, err: err, pane: p}
	}
}
func (m *Model) sortDiagrams() {
	sort.SliceStable(m.diagrams, func(i, j int) bool {
		a, b := m.diagrams[i], m.diagrams[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.StartByte < b.StartByte
	})
}
func (m *Model) invalidatePreview() {
	m.revision++
	m.pending = false
	m.stale = true
	if m.cancelRender != nil {
		m.cancelRender()
		m.cancelRender = nil
	}
}

func (m *Model) waitEditorEvent() tea.Cmd {
	if m.replayingSnapshot || m.pane == nil {
		return nil
	}
	return m.pane.WaitEvent()
}
