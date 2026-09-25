// Package editor embeds a real isolated Neovim TUI. PTY display belongs to
// bubbleterm; its independent RPC connection carries unsaved document state.
package editor

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/neovim/go-client/nvim"
	"github.com/taigrr/bubbleterm"
	"github.com/taigrr/bubbleterm/emulator"

	"lazymermaid/internal/document"
)

//go:embed init.lua
var initLua []byte

var ErrDirty = errors.New("editor has unsaved buffers")

type Options struct {
	Width, Height int
	Directory     string
	NvimPath      string
	// RuntimePaths are application-managed parser/query paths, never user config.
	RuntimePaths []string
}

type Snapshot struct {
	Buffer        int    `json:"buffer"`
	Path          string `json:"path"`
	Source        string `json:"source"`
	ChangedTick   int    `json:"changed_tick"`
	CursorLine    int    `json:"cursor_line"` // 1-based line in this source buffer
	Dirty         bool   `json:"dirty"`
	Virtual       bool   `json:"virtual"`
	ParentPath    string `json:"parent_path"`
	BodyStartLine int    `json:"body_start_line"`
}

type Event struct {
	Kind     string // changed, exit, or error
	Snapshot Snapshot
	Err      error
}

type DirtyBuffer struct {
	Buffer  int    `json:"buffer"`
	Path    string `json:"path"`
	Virtual bool   `json:"virtual"`
}

// Diagnostic uses 1-based lines and byte columns in the snapshot's source.
type Diagnostic struct {
	Line, Column, EndLine, EndColumn int
	Message, Source                  string
}

type virtualSession struct {
	path, parent, diskHash string
	parentTick             int
	diagram                document.Diagram
}

// Pane's Update/View/Resize/Focus/Blur methods belong to the UI goroutine.
// RPC methods may run in effects. Events and Snapshot are concurrency-safe.
type Pane struct {
	term    *bubbleterm.Model
	rpc     *nvim.Nvim
	runtime string
	events  chan Event
	done    chan struct{}
	close   sync.Once
	mu      sync.RWMutex
	current Snapshot
	virtual map[string]*virtualSession
	nextID  int
	dir     string
}

// New starts Neovim and connects RPC. Call it from a background effect, since
// process startup and socket readiness perform I/O.
func New(ctx context.Context, opts Options) (_ *Pane, err error) {
	if opts.NvimPath == "" {
		opts.NvimPath = "nvim"
	}
	if opts.Width < 1 {
		opts.Width = 80
	}
	if opts.Height < 1 {
		opts.Height = 24
	}
	if opts.Directory == "" {
		opts.Directory, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp("", "lazymermaid-editor-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if err = os.WriteFile(filepath.Join(dir, "init.lua"), initLua, 0600); err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "nvim.sock")
	cmd := exec.Command(opts.NvimPath, "-u", filepath.Join(dir, "init.lua"), "-i", "NONE", "--noplugin", "--listen", socket)
	cmd.Dir = opts.Directory
	cmd.Env = editorEnv(os.Environ())
	term, err := bubbleterm.NewWithCommand(opts.Width, opts.Height, cmd)
	if err != nil {
		return nil, fmt.Errorf("start Neovim: %w", err)
	}
	p := &Pane{term: term, runtime: dir, events: make(chan Event, 64), done: make(chan struct{}), virtual: make(map[string]*virtualSession), dir: opts.Directory}
	defer func() {
		if err != nil {
			_ = term.Close()
			if p.rpc != nil {
				_ = p.rpc.Close()
			}
		}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		p.rpc, err = nvim.Dial(socket, nvim.DialContext(ctx), nvim.DialLogf(func(string, ...interface{}) {}))
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("connect Neovim RPC: %w", err)
		case <-tick.C:
			if term.GetEmulator().IsProcessExited() {
				return nil, errors.New("Neovim exited during startup")
			}
		}
	}
	if err = p.rpc.RegisterHandler("lazymermaid_changed", p.changed); err != nil {
		return nil, err
	}
	if err = p.rpc.RegisterHandler("lazymermaid_save_virtual", p.prepareVirtual); err != nil {
		return nil, err
	}
	if err = p.rpc.RegisterHandler("lazymermaid_virtual_saved", p.virtualSaved); err != nil {
		return nil, err
	}
	if err = p.rpc.ExecLua("lazymermaid.connect(...)", nil, p.rpc.ChannelID(), opts.RuntimePaths); err != nil {
		return nil, fmt.Errorf("initialize Neovim: %w", err)
	}
	term.GetEmulator().SetOnExit(func(string) { p.publish(Event{Kind: "exit"}) })
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close(true)
		case <-p.done:
		}
	}()
	return p, nil
}

func editorEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "TERM", "COLORTERM", "NVIM", "NVIM_LISTEN_ADDRESS", "VIMINIT", "EXINIT", "MYVIMRC", "VIMRUNTIME":
			continue
		}
		out = append(out, item)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
}

func (p *Pane) changed(payload string) {
	var s Snapshot
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		p.publish(Event{Kind: "error", Err: err})
		return
	}
	p.mu.Lock()
	p.current = s
	p.mu.Unlock()
	p.publish(Event{Kind: "changed", Snapshot: s})
}

func (p *Pane) publish(ev Event) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.events <- ev:
	default:
		// Fast typing may supersede queued snapshots; the latest complete one is
		// always retained. Nothing here blocks Neovim's RPC dispatch.
		select {
		case <-p.events:
		default:
		}
		select {
		case p.events <- ev:
		default:
		}
	}
}

func (p *Pane) Events() <-chan Event { return p.events }
func (p *Pane) WaitEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-p.events:
			return ev
		case <-p.done:
			return Event{Kind: "exit"}
		}
	}
}
func (p *Pane) Snapshot() Snapshot { p.mu.RLock(); defer p.mu.RUnlock(); return p.current }

// CurrentSnapshot reads the currently displayed Neovim document over RPC.
// Call it from an effect after navigation when queued change events may still
// describe the previous document. It does not alter the cached event snapshot.
func (p *Pane) CurrentSnapshot() (Snapshot, error) {
	var payload string
	if err := p.rpc.ExecLua("return vim.json.encode(lazymermaid.snapshot())", &payload); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	err := json.Unmarshal([]byte(payload), &snapshot)
	return snapshot, err
}

func (p *Pane) Init() tea.Cmd { return p.term.Init() }
func (p *Pane) Focus()        { p.term.Focus() }
func (p *Pane) Blur()         { p.term.Blur() }
func (p *Pane) Focused() bool { return p.term.Focused() }

// Update accepts pane-local mouse coordinates. The host filters its own global
// shortcuts before calling this method, and keeps forwarding output messages
// even while the pane is blurred.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyReleaseMsg, tea.WindowSizeMsg:
		return nil
	case tea.PasteMsg:
		if !p.Focused() {
			return nil
		}
		return func() tea.Msg {
			_, err := p.rpc.Paste(msg.Content, false, -1)
			if err != nil {
				return Event{Kind: "error", Err: err}
			}
			return nil
		}
	}
	_, cmd := p.term.Update(msg)
	return cmd
}

func (p *Pane) Resize(w, h int) tea.Cmd { return p.term.Resize(max(1, w), max(1, h)) }

// View's cursor coordinates are local to the editor content rectangle.
func (p *Pane) View() tea.View {
	v := p.term.View()
	pos, visible := p.term.GetEmulator().Cursor()
	if p.Focused() && visible {
		a := p.term.GetEmulator().CursorAppearance()
		v.Cursor = tea.NewCursor(pos.X, pos.Y)
		v.Cursor.Blink = a.Blink
		v.Cursor.Color = a.Color
		switch a.Style {
		case emulator.CursorBar:
			v.Cursor.Shape = tea.CursorBar
		case emulator.CursorUnderline:
			v.Cursor.Shape = tea.CursorUnderline
		default:
			v.Cursor.Shape = tea.CursorBlock
		}
	}
	return v
}

func (p *Pane) Open(path string, line, col int) error {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.dir, path)
	}
	return p.rpc.ExecLua("lazymermaid.open(...)", nil, path, max(1, line), max(1, col))
}
func (p *Pane) OpenScratch(source string) error {
	return p.rpc.ExecLua("lazymermaid.scratch(...)", nil, source)
}

func (p *Pane) OpenVirtual(diagram document.Diagram) error {
	if !diagram.VirtualEditable || !diagram.Closed || !diagram.TopLevel || diagram.Standalone {
		return errors.New("virtual editing requires a closed top-level Mermaid fence")
	}
	path := diagram.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.dir, path)
	}
	var payload string
	if err := p.rpc.ExecLua("return lazymermaid.parent(...)", &payload, path); err != nil {
		return err
	}
	var parent Snapshot
	if err := json.Unmarshal([]byte(payload), &parent); err != nil {
		return err
	}
	doc, err := document.Parse(path, []byte(parent.Source))
	if err != nil {
		return err
	}
	var selected *document.Diagram
	for i := range doc.Diagrams {
		d := &doc.Diagrams[i]
		if d.StartLine == diagram.StartLine && normalize(d.Source) == normalize(diagram.Source) {
			selected = d
			break
		}
	}
	if selected == nil || !selected.VirtualEditable {
		return errors.New("source changed since discovery; rescan before virtual editing")
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Inspect original bytes too: Neovim can normalize line endings while loading,
	// which must not turn a mixed-EOL document into an editable projection.
	diskDoc, err := document.Parse(path, disk)
	if err != nil {
		return err
	}
	if diskDoc.Newline == "mixed" {
		return errors.New("virtual editing is unavailable for mixed line endings; use raw source")
	}
	p.mu.Lock()
	p.nextID++
	token := fmt.Sprintf("block-%d", p.nextID)
	p.virtual[token] = &virtualSession{path: path, parent: parent.Source, parentTick: parent.ChangedTick, diskHash: digest(disk), diagram: *selected}
	p.mu.Unlock()
	if err := p.rpc.ExecLua("lazymermaid.open_virtual(...)", nil, token, path, parent.Buffer, parent.ChangedTick, selected.Source, selected.BodyStartLine); err != nil {
		p.mu.Lock()
		delete(p.virtual, token)
		p.mu.Unlock()
		return err
	}
	return nil
}

type virtualRequest struct {
	Token      string `json:"token"`
	Parent     string `json:"parent"`
	ParentTick int    `json:"parent_tick"`
	Body       string `json:"body"`
}

// These synchronous RPC handlers never call Neovim: BufWriteCmd is waiting for
// their reply. Only the Lua caller applies the patch and invokes native :write.
func (p *Pane) prepareVirtual(payload string) (string, error) {
	var req virtualRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", err
	}
	p.mu.RLock()
	session := p.virtual[req.Token]
	var saved virtualSession
	if session != nil {
		saved = *session
	}
	p.mu.RUnlock()
	var candidate string
	var err error
	if session == nil {
		err = errors.New("virtual session no longer exists")
	} else if req.ParentTick != saved.parentTick || req.Parent != saved.parent {
		err = errors.New("source buffer changed")
	} else if disk, readErr := os.ReadFile(saved.path); readErr != nil {
		err = readErr
	} else if digest(disk) != saved.diskHash {
		err = errors.New("source file changed on disk")
	} else {
		candidate, err = document.PrepareEdit(saved.path, req.Parent, saved.diagram, req.Body)
	}
	result := map[string]string{"source": candidate}
	if err != nil {
		result["error"] = err.Error()
	}
	encoded, _ := json.Marshal(result)
	return string(encoded), nil
}

func (p *Pane) virtualSaved(payload string) (bool, error) {
	var req virtualRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return false, err
	}
	p.mu.RLock()
	old := p.virtual[req.Token]
	var saved virtualSession
	if old != nil {
		saved = *old
	}
	p.mu.RUnlock()
	if old == nil {
		return false, errors.New("virtual session no longer exists")
	}
	doc, err := document.Parse(saved.path, []byte(req.Parent))
	if err != nil {
		return false, err
	}
	disk, err := os.ReadFile(saved.path)
	if err != nil {
		return false, err
	}
	for _, d := range doc.Diagrams {
		if d.StartLine == saved.diagram.StartLine {
			saved.parent, saved.parentTick, saved.diskHash, saved.diagram = req.Parent, req.ParentTick, digest(disk), d
			p.mu.Lock()
			p.virtual[req.Token] = &saved
			p.mu.Unlock()
			return true, nil
		}
	}
	return false, errors.New("saved Mermaid block was not found")
}

func (p *Pane) SetDiagnostics(snapshot Snapshot, diagnostics []Diagnostic) error {
	items := make([]map[string]interface{}, 0, len(diagnostics))
	for _, d := range diagnostics {
		items = append(items, map[string]interface{}{"lnum": max(0, d.Line-1), "col": max(0, d.Column-1), "end_lnum": max(0, d.Line-1, d.EndLine-1), "end_col": max(1, d.Column, d.EndColumn-1), "message": d.Message, "source": d.Source, "severity": 1})
	}
	return p.rpc.ExecLua("lazymermaid.diagnostics(...)", nil, snapshot.Buffer, snapshot.ChangedTick, items)
}

func (p *Pane) DirtyBuffers() ([]DirtyBuffer, error) {
	var payload string
	if err := p.rpc.ExecLua("return lazymermaid.dirty()", &payload); err != nil {
		return nil, err
	}
	var result []DirtyBuffer
	err := json.Unmarshal([]byte(payload), &result)
	return result, err
}

// Close refuses unsaved work unless force is explicitly chosen by the caller.
// Parent context cancellation is reserved for application teardown.
func (p *Pane) Close(force bool) error {
	if !force && !p.term.GetEmulator().IsProcessExited() {
		dirty, err := p.DirtyBuffers()
		if err != nil {
			return err
		}
		if len(dirty) > 0 {
			return ErrDirty
		}
	}
	p.close.Do(func() {
		close(p.done)
		if p.rpc != nil {
			_, _ = p.rpc.Input("<Esc>:qa!<CR>")
			_ = p.rpc.Close()
		}
		_ = p.term.Close()
		_ = os.RemoveAll(p.runtime)
	})
	return nil
}

func digest(data []byte) string    { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func normalize(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }
