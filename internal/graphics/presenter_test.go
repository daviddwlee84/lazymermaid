package graphics

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func graphicsCommands(t *testing.T, s string) []kitty.Options {
	t.Helper()
	var commands []kitty.Options
	parser := ansi.NewParser()
	parser.SetDataSize(1 << 20)
	parser.SetHandler(ansi.Handler{HandleApc: func(data []byte) {
		if !bytes.HasPrefix(data, []byte("G")) {
			return
		}
		options := bytes.SplitN(data[1:], []byte(";"), 2)[0]
		var command kitty.Options
		if err := command.UnmarshalText(options); err != nil {
			t.Fatal(err)
		}
		if command.Action == 0 {
			command.Action = kitty.Transmit
		}
		commands = append(commands, command)
	}})
	for _, b := range []byte(s) {
		parser.Advance(b)
	}
	return commands
}
func count(commands []kitty.Options, action byte) int {
	n := 0
	for _, c := range commands {
		if c.Action == action {
			n++
		}
	}
	return n
}
func lastPut(t *testing.T, s string) kitty.Options {
	t.Helper()
	var put kitty.Options
	for _, c := range graphicsCommands(t, s) {
		if c.Action == kitty.Put {
			put = c
		}
	}
	if put.Action != kitty.Put {
		t.Fatal("missing placement")
	}
	return put
}
func shown(t *testing.T, w, h int) (*Presenter, *bytes.Buffer) {
	t.Helper()
	t.Setenv("TMUX", "")
	out := new(bytes.Buffer)
	p := New(out)
	if err := p.SetPNG(pngBytes(t, w, h)); err != nil {
		t.Fatal(err)
	}
	p.Configure(Rect{5, 4, 20, 20}, true)
	if n, err := p.Write([]byte("FRAME")); n != 5 || err != nil {
		t.Fatalf("write = %d,%v", n, err)
	}
	return p, out
}

func TestFramePlacementAndOwnedCleanup(t *testing.T) {
	p, out := shown(t, 20, 20)
	if !strings.HasPrefix(out.String(), "FRAME") {
		t.Fatal("graphics preceded framework frame")
	}
	if !strings.Contains(out.String(), ansi.CursorPosition(6, 5)) {
		t.Fatal("wrong pane origin")
	}
	commands := graphicsCommands(t, out.String())
	if count(commands, kitty.Transmit) != 1 || count(commands, kitty.Put) != 1 {
		t.Fatalf("unexpected graphics operations: %+v", commands)
	}
	for _, c := range commands {
		if c.ID != p.imageID || c.Quiet != 2 {
			t.Fatalf("wrong ownership/quiet mode: %+v", c)
		}
	}
	out.Reset()
	p.Configure(Rect{}, false)
	_, _ = p.Write([]byte("MODAL"))
	commands = graphicsCommands(t, out.String())
	if count(commands, kitty.Put) != 0 || count(commands, kitty.Delete) != 1 {
		t.Fatalf("image leaked over modal: %+v", commands)
	}
	out.Reset()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	commands = graphicsCommands(t, out.String())
	if len(commands) != 1 || commands[0].Delete != kitty.DeleteID || !commands[0].DeleteResources || commands[0].ID != p.imageID {
		t.Fatalf("cleanup did not free only owned image: %+v", commands)
	}
	before := out.Len()
	if err := p.Close(); err != nil || out.Len() != before {
		t.Fatal("close must be idempotent")
	}
	if q := New(io.Discard); q.imageID == p.imageID {
		t.Fatal("presenters must have distinct IDs")
	}
}

func TestClearScreenAcrossWritesAndHiddenModal(t *testing.T) {
	p, out := shown(t, 20, 20)
	out.Reset()
	if _, err := p.Write([]byte("\x1b[")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "\x1b[" {
		t.Fatal("graphics inserted inside a partial CSI")
	}
	if _, err := p.Write([]byte("2JFRAME")); err != nil {
		t.Fatal(err)
	}
	if count(graphicsCommands(t, out.String()), kitty.Transmit) != 1 {
		t.Fatal("split clear-screen did not reupload image")
	}
	out.Reset()
	p.Configure(Rect{5, 4, 20, 20}, false)
	_, _ = p.Write([]byte(ansi.EraseEntireScreen + "MODAL"))
	if count(graphicsCommands(t, out.String()), kitty.Put) != 0 {
		t.Fatal("image displayed while hidden")
	}
	out.Reset()
	p.Configure(Rect{5, 4, 20, 20}, true)
	_, _ = p.Write([]byte("RETURN"))
	if count(graphicsCommands(t, out.String()), kitty.Transmit) != 1 {
		t.Fatal("clear while hidden lost upload invalidation")
	}
}

func TestAlternateScreenExitDoesNotPaintShell(t *testing.T) {
	p, out := shown(t, 20, 20)
	out.Reset()
	_, _ = p.Write([]byte(ansi.ResetModeAltScreenSaveCursor))
	if count(graphicsCommands(t, out.String()), kitty.Put) != 0 {
		t.Fatal("image repainted on exit")
	}
	out.Reset()
	_, _ = p.Write([]byte("shell prompt"))
	if out.String() != "shell prompt" {
		t.Fatal("graphics leaked after alternate-screen exit")
	}
	out.Reset()
	_, _ = p.Write([]byte(ansi.SetModeAltScreenSaveCursor + "RESUME"))
	commands := graphicsCommands(t, out.String())
	if count(commands, kitty.Transmit) != 1 || count(commands, kitty.Put) != 1 {
		t.Fatal("image not restored after reenter")
	}
}

func TestCameraRefreshAspectAndBoundedPan(t *testing.T) {
	p, out := shown(t, 400, 400)
	p.CellSize(10, 10)
	out.Reset()
	p.Zoom(2)
	if out.Len() == 0 {
		t.Fatal("zoom requires refresh even when Bubble Tea skips identical text frame")
	}
	put := lastPut(t, out.String())
	if put.Width != 200 || put.Height != 200 {
		t.Fatalf("wrong zoom crop: %+v", put)
	}
	if count(graphicsCommands(t, out.String()), kitty.Transmit) != 0 {
		t.Fatal("camera controls must not reupload image")
	}
	p.Pan(math.MaxInt, math.MaxInt)
	if p.panX != 200 || p.panY != 200 {
		t.Fatal("pan did not clamp to source")
	}
	p.Pan(-1, -1)
	if p.panX != 180 || p.panY != 180 {
		t.Fatal("edge accumulated invisible pan")
	}
	p.Zoom(math.NaN())
	p.Zoom(math.Inf(1))
	p.Zoom(-1)
	if p.zoom != 2 {
		t.Fatal("invalid zoom corrupted camera")
	}
	out.Reset()
	p.Fit()
	put = lastPut(t, out.String())
	if put.Width != 400 || put.Height != 400 || put.X != 0 || put.Y != 0 {
		t.Fatalf("fit did not restore full source: %+v", put)
	}
	if put.Columns != 0 && put.Rows != 0 {
		t.Fatal("known cell metrics should preserve aspect with one display axis")
	}
	p2, out2 := shown(t, 1000, 1)
	p2.CellSize(8, 16)
	out2.Reset()
	p2.Fit()
	put = lastPut(t, out2.String())
	if put.Columns != 20 || put.Rows != 0 {
		t.Fatalf("panorama would be stretched to a full cell row: %+v", put)
	}
}

func TestTmuxUsesUpstreamPassthrough(t *testing.T) {
	t.Setenv("TMUX", "fixture")
	var out bytes.Buffer
	p := New(&out)
	if err := p.SetPNG(pngBytes(t, 20, 20)); err != nil {
		t.Fatal(err)
	}
	p.Configure(Rect{1, 2, 10, 5}, true)
	_, _ = p.Write([]byte("FRAME"))
	if !strings.HasPrefix(p.encoded, "\x1bPtmux;") || !strings.Contains(out.String(), p.encoded) {
		t.Fatal("image transmission is not wrapped")
	}
	put := kitty.Options{Action: kitty.Put, ID: p.imageID, PlacementID: 1, Width: 20, Height: 20, Columns: 10, Rows: 5, DoNotMoveCursor: true, Quiet: 2}
	if !strings.Contains(out.String(), ansi.TmuxPassthrough(ansi.KittyGraphics(nil, put.Options()...))) {
		t.Fatal("placement is not wrapped by upstream helper")
	}
	out.Reset()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	del := kitty.Options{Action: kitty.Delete, ID: p.imageID, Delete: kitty.DeleteID, DeleteResources: true, Quiet: 2}
	if out.String() != ansi.TmuxPassthrough(ansi.KittyGraphics(nil, del.Options()...)) {
		t.Fatal("cleanup must use same tmux transport")
	}
}

type shortWriter struct{ limit int }

func (w shortWriter) Write(b []byte) (int, error) { return min(len(b), w.limit), nil }
func TestShortWritesDoNotClaimUploadSuccess(t *testing.T) {
	for _, limit := range []int{2, 8} {
		t.Run(string(rune('a'+limit)), func(t *testing.T) {
			t.Setenv("TMUX", "")
			p := New(shortWriter{limit})
			_ = p.SetPNG(pngBytes(t, 20, 20))
			p.Configure(Rect{0, 0, 10, 10}, true)
			n, err := p.Write([]byte("FRAME"))
			if n != min(5, limit) || !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("write = %d,%v", n, err)
			}
			if p.uploaded || !p.dirty {
				t.Fatal("partial graphics write committed success state")
			}
			if n, err := p.Write([]byte("next")); n != 0 || !errors.Is(err, io.ErrShortWrite) {
				t.Fatal("failed stream must not accept another frame")
			}
		})
	}
}

func TestTerminalIdentityForwardingAndCloseOwnership(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	p := New(file)
	if p.Fd() != file.Fd() {
		t.Fatal("lost terminal file descriptor")
	}
	if _, err := file.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	_, _ = file.Seek(0, 0)
	b := make([]byte, 4)
	if n, err := p.Read(b); n != 4 || err != nil || string(b) != "data" {
		t.Fatalf("read forwarding: %d,%v,%q", n, err, b)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("still open"); err != nil {
		t.Fatal("presenter closed caller-owned stdout")
	}
	if q := New(io.Discard); q.Fd() != ^uintptr(0) {
		t.Fatal("non-terminal writer has fabricated descriptor")
	}
}

func TestConcurrentControlsAndFrames(t *testing.T) {
	t.Setenv("TMUX", "")
	var out bytes.Buffer
	p := New(&out)
	data := pngBytes(t, 50, 50)
	_ = p.SetPNG(data)
	p.Configure(Rect{0, 0, 20, 10}, true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch i {
				case 0:
					_, _ = p.Write([]byte("FRAME"))
				case 1:
					p.Pan(1, -1)
					p.Zoom(1.1)
					p.Fit()
				case 2:
					p.Configure(Rect{1, 2, 20, 10}, j%2 == 0)
				case 3:
					_ = p.SetPNG(data)
				}
			}
		}(i)
	}
	wg.Wait()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}
