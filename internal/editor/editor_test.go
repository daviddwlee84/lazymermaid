package editor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"lazymermaid/internal/document"
)

func newTestPane(t *testing.T) *Pane {
	t.Helper()
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("Neovim is not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p, err := New(ctx, Options{Directory: t.TempDir(), Width: 80, Height: 24})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(true); cancel() })
	return p
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not observed within 3 seconds")
}

func TestRealNeovimUnsavedUnicodePasteAndCloseGuard(t *testing.T) {
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "nvim"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "nvim", "init.lua"), []byte("vim.g.lazymermaid_user_config = true"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", config)
	p := newTestPane(t)
	var userConfig bool
	if err := p.rpc.ExecLua("return vim.g.lazymermaid_user_config == true", &userConfig); err != nil {
		t.Fatal(err)
	}
	if userConfig {
		t.Fatal("loaded user init.lua")
	}
	path := filepath.Join(p.dir, "diagram.mmd")
	original := "flowchart LR\n  A --> B\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(path, 2, 1); err != nil {
		t.Fatal(err)
	}
	fresh, err := p.CurrentSnapshot()
	if err != nil || filepath.Base(fresh.Path) != filepath.Base(path) || fresh.Source != original || fresh.CursorLine != 2 {
		t.Fatalf("fresh navigation snapshot: %#v %v", fresh, err)
	}
	eventually(t, func() bool { return filepath.Base(p.Snapshot().Path) == filepath.Base(path) })
	if got := p.Snapshot().CursorLine; got != 2 {
		t.Fatalf("opening line 2 reported cursor line %d", got)
	}
	if err := p.rpc.Command("normal! G$"); err != nil {
		t.Fatal(err)
	}
	cmd := p.Update(tea.PasteMsg{Content: "\n  B --> C[中文 e\u0301 👋🏿]"})
	if cmd == nil {
		t.Fatal("paste was not handled")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("paste failed: %#v", msg)
	}
	eventually(t, func() bool { return strings.Contains(p.Snapshot().Source, "中文") && p.Snapshot().Dirty })
	if got := p.Snapshot().CursorLine; got != 3 {
		t.Fatalf("changed snapshot should capture new cursor line 3, got %d", got)
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Fatal("unsaved preview edited the disk")
	}
	if !errors.Is(p.Close(false), ErrDirty) {
		t.Fatal("Close did not protect unsaved data")
	}
	if p.Update(tea.KeyReleaseMsg{Code: 'x', Text: "x"}) != nil {
		t.Fatal("key release was forwarded")
	}
	// Exercise real PTY resize and wide-character screen rendering.
	if cmd := p.Resize(96, 30); cmd != nil {
		_ = cmd()
	}
	eventually(t, func() bool {
		frame := p.term.GetEmulator().GetScreen()
		return len(frame.Rows) == 30 && strings.Contains(strings.Join(frame.Rows, "\n"), "中文")
	})
	if err := p.rpc.Command("write"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !p.Snapshot().Dirty })
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "中文") {
		t.Fatal("native write did not save")
	}
}

func TestRealNeovimVirtualSaveAndDiskConflict(t *testing.T) {
	p := newTestPane(t)
	path := filepath.Join(p.dir, "readme.md")
	input := "# Diagram\n\n```mermaid\nflowchart LR\n  A --> B\n```\n\nUntouched.\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.Parse(path, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagrams) != 1 {
		t.Fatalf("unexpected diagrams: %#v", doc.Diagrams)
	}
	if err := p.OpenVirtual(doc.Diagrams[0]); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return p.Snapshot().Virtual })
	buf, err := p.rpc.CurrentBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.SetBufferLines(buf, 0, -1, true, [][]byte{[]byte("flowchart LR"), []byte("  A --> C[中文]")}); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(input, "  A --> B", "  A --> C[中文]", 1)
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("virtual save changed surrounding source\nwant %q\ngot  %q", want, string(got))
	}
	eventually(t, func() bool { return !p.Snapshot().Dirty })
	// A second save exercises the acknowledged new baseline.
	if err := p.rpc.SetBufferLines(buf, 1, 2, true, [][]byte{[]byte("  A --> D")}); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.SetBufferLines(buf, 1, 2, true, [][]byte{[]byte("  A --> E")}); err != nil {
		t.Fatal(err)
	}
	external := "# external change\n" + want
	if err := os.WriteFile(path, []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("expected disk conflict, got %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != external {
		t.Fatal("virtual conflict overwrote disk")
	}
	eventually(t, func() bool { return p.Snapshot().Dirty && strings.Contains(p.Snapshot().Source, "A --> E") })
}

func TestRealNeovimScratchCanWriteFilename(t *testing.T) {
	p := newTestPane(t)
	if err := p.OpenScratch("flowchart LR\nA --> B\n"); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write scratch.mmd"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(p.dir, "scratch.mmd")); err != nil || !strings.Contains(string(got), "A --> B") {
		t.Fatalf("scratch write: %q %v", got, err)
	}
}

func TestRealNeovimVirtualNativeWriteFailureRetainsBothBuffersAndDetaches(t *testing.T) {
	p := newTestPane(t)
	path := filepath.Join(p.dir, "readonly.md")
	input := "```mermaid\nflowchart LR\nA --> B\n```\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.Parse(path, []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.OpenVirtual(doc.Diagrams[0]); err != nil {
		t.Fatal(err)
	}
	buf, _ := p.rpc.CurrentBuffer()
	if err := p.rpc.SetBufferLines(buf, 1, 2, true, [][]byte{[]byte("A --> C")}); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.ExecLua("local v=lazymermaid.virtual[vim.api.nvim_get_current_buf()]; vim.bo[v.parent].readonly=true", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err == nil {
		t.Fatal("readonly parent write unexpectedly succeeded")
	}
	if got, _ := os.ReadFile(path); string(got) != input {
		t.Fatal("failed write changed disk")
	}
	eventually(t, func() bool { return p.Snapshot().Dirty && strings.Contains(p.Snapshot().Source, "A --> C") })
	var retained bool
	if err := p.rpc.ExecLua("local v=lazymermaid.virtual[vim.api.nvim_get_current_buf()]; return v.detached and vim.bo[v.parent].modified and lazymermaid.snapshot(v.parent).source:find('A --> C',1,true) ~= nil", &retained); err != nil || !retained {
		t.Fatalf("applied parent was not retained dirty and detached: %v %v", retained, err)
	}
	dirty, err := p.DirtyBuffers()
	if err != nil || len(dirty) != 2 {
		t.Fatalf("expected dirty source and virtual buffers: %#v %v", dirty, err)
	}
	if err := p.rpc.ExecLua("local v=lazymermaid.virtual[vim.api.nvim_get_current_buf()]; vim.bo[v.parent].readonly=false", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("old projection allowed retry after failed native write: %v", err)
	}
	if err := p.Open(path, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.Command("write"); err != nil {
		t.Fatalf("native parent recovery failed: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(written), "A --> C") {
		t.Fatalf("native parent recovery lost applied edit: %q %v", written, err)
	}
	updated, err := document.Parse(path, written)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.OpenVirtual(updated.Diagrams[0]); err != nil {
		t.Fatalf("reopening saved block failed: %v", err)
	}
	if dirty, err := p.DirtyBuffers(); err != nil || len(dirty) != 1 || !dirty[0].Virtual {
		t.Fatalf("old virtual draft should remain available after reopen: %#v %v", dirty, err)
	}
}

func TestRealNeovimVirtualInvalidatesRenameAndSerializationDrift(t *testing.T) {
	cases := []struct{ name, mutate, message string }{
		{"rename", "vim.api.nvim_buf_set_name(v.parent, v.path .. '.renamed')", "renamed"},
		{"fileformat", "vim.bo[v.parent].fileformat = 'dos'", "serialization options changed"},
		{"fileencoding", "vim.bo[v.parent].fileencoding = 'utf-16le'", "serialization options changed"},
		{"endofline", "vim.bo[v.parent].endofline = not vim.bo[v.parent].endofline", "serialization options changed"},
		{"fixendofline", "vim.bo[v.parent].fixendofline = not vim.bo[v.parent].fixendofline", "serialization options changed"},
		{"bom", "vim.bo[v.parent].bomb = not vim.bo[v.parent].bomb", "serialization options changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPane(t)
			path := filepath.Join(p.dir, "source.md")
			input := "```mermaid\nflowchart LR\nA --> B\n```\n"
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			doc, err := document.Parse(path, []byte(input))
			if err != nil {
				t.Fatal(err)
			}
			if err := p.OpenVirtual(doc.Diagrams[0]); err != nil {
				t.Fatal(err)
			}
			if err := p.rpc.SetBufferLines(0, 1, 2, true, [][]byte{[]byte("A --> DRAFT")}); err != nil {
				t.Fatal(err)
			}
			var tickUnchanged bool
			if err := p.rpc.ExecLua("local v=lazymermaid.virtual[vim.api.nvim_get_current_buf()]; local tick=vim.api.nvim_buf_get_changedtick(v.parent); "+tc.mutate+"; return tick==vim.api.nvim_buf_get_changedtick(v.parent)", &tickUnchanged); err != nil {
				t.Fatal(err)
			}
			if !tickUnchanged {
				t.Fatal("fixture should change metadata without changing changedtick")
			}
			if err := p.rpc.Command("write"); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %q conflict, got %v", tc.message, err)
			}
			if got, _ := os.ReadFile(path); string(got) != input {
				t.Fatal("conflict changed original disk source")
			}
			var preserved bool
			if err := p.rpc.ExecLua("local b=vim.api.nvim_get_current_buf(); return lazymermaid.virtual[b].detached and vim.bo[b].modified and lazymermaid.snapshot(b).source:find('DRAFT',1,true) ~= nil", &preserved); err != nil || !preserved {
				t.Fatalf("projection not detached with draft preserved: %v %v", preserved, err)
			}
		})
	}
}

func TestRealNeovimVirtualRejectsMixedLineEndings(t *testing.T) {
	p := newTestPane(t)
	path := filepath.Join(p.dir, "mixed.md")
	good := "```mermaid\nflowchart LR\nA --> B\n```\n"
	doc, err := document.Parse(path, []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("```mermaid\r\nflowchart LR\nA --> B\n```\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.OpenVirtual(doc.Diagrams[0]); err == nil {
		t.Fatal("mixed line endings became editable after loading into Neovim")
	}
}

func TestRealNeovimIgnoresStaleDiagnostics(t *testing.T) {
	p := newTestPane(t)
	if err := p.OpenScratch("flowchart LR\nA --> B\n"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return strings.Contains(p.Snapshot().Source, "A --> B") })
	snapshot := p.Snapshot()
	if err := p.SetDiagnostics(snapshot, []Diagnostic{{Line: 2, Column: 1, Message: "upstream error", Source: "mermaid"}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.rpc.ExecLua("return #vim.diagnostic.get(0)", &count); err != nil || count != 1 {
		t.Fatalf("diagnostics: %d %v", count, err)
	}
	if err := p.rpc.SetBufferLines(0, 1, 2, true, [][]byte{[]byte("A --> C")}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetDiagnostics(snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.rpc.ExecLua("return #vim.diagnostic.get(0)", &count); err != nil || count != 1 {
		t.Fatalf("stale update replaced diagnostics: %d %v", count, err)
	}
}

func TestEditorEnvFiltersParentNvim(t *testing.T) {
	got := editorEnv([]string{"PATH=/bin", "NVIM=/unsafe/socket", "TERM=ghostty", "VIMINIT=unsafe", "KEEP=yes"})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "unsafe") || strings.Contains(joined, "TERM=ghostty") {
		t.Fatalf("unsafe child environment: %v", got)
	}
	if !strings.Contains(joined, "KEEP=yes") || !strings.Contains(joined, "TERM=xterm-256color") {
		t.Fatal(got)
	}
}
