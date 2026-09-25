// Package graphics displays upstream-rendered PNGs. Charm owns graphics and
// ANSI encoding; this package only owns a preview's position and lifecycle.
package graphics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"image"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/charmbracelet/x/ansi/parser"
)

type Rect struct{ X, Y, Width, Height int }
type Presenter struct {
	mu                               sync.Mutex
	out                              io.Writer
	tmux                             bool
	imageID                          int
	visible, dirty, closed           bool
	uploaded, placed, terminalActive bool
	encoded                          string
	bounds                           image.Rectangle
	rect                             Rect
	zoom                             float64
	panX, panY                       int
	cellWidth, cellHeight            int
	cellKnown                        bool
	stream                           *ansi.Parser
	writeErr                         error
}

var ids atomic.Uint32

func init() {
	var seed [4]byte
	if _, err := rand.Read(seed[:]); err != nil {
		binary.BigEndian.PutUint32(seed[:], uint32(time.Now().UnixNano())^uint32(os.Getpid()))
	}
	ids.Store(binary.BigEndian.Uint32(seed[:]) & math.MaxInt32)
}
func nextID() int {
	for {
		if id := ids.Add(1) & math.MaxInt32; id != 0 {
			return int(id)
		}
	}
}
func New(out io.Writer) *Presenter {
	p := &Presenter{out: out, tmux: os.Getenv("TMUX") != "", imageID: nextID(), zoom: 1, cellWidth: 8, cellHeight: 16, terminalActive: true}
	if w, h := terminalCellSize(out); w > 0 && h > 0 {
		p.cellWidth, p.cellHeight, p.cellKnown = w, h, true
	}
	p.stream = ansi.NewParser()
	p.stream.SetDataSize(256)
	p.stream.SetHandler(ansi.Handler{HandleCsi: p.observeCSI, HandleEsc: p.observeESC})
	return p
}

// Fd and Read preserve Bubble Tea's term.File detection and size queries.
// Close cleans owned graphics but never closes the underlying stdout terminal.
func (p *Presenter) Fd() uintptr {
	if f, ok := p.out.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0)
}
func (p *Presenter) Read(b []byte) (int, error) {
	if r, ok := p.out.(io.Reader); ok {
		return r.Read(b)
	}
	return 0, io.EOF
}

// Available is an environment hint, not an active graphics-protocol probe.
// It never reads stdin or changes tmux options.
func Available() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	compatible := os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty") || strings.Contains(term, "kitty") || strings.Contains(term, "ghostty")
	if !compatible {
		return false
	}
	if os.Getenv("TMUX") != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		data, err := exec.CommandContext(ctx, "tmux", "show-option", "-p", "-v", "allow-passthrough").Output()
		value := strings.TrimSpace(string(data))
		return err == nil && (value == "on" || value == "all")
	}
	return true
}
func (p *Presenter) wrap(s string) string {
	if p.tmux {
		return ansi.TmuxPassthrough(s)
	}
	return s
}
func (p *Presenter) delete(resources bool) string {
	o := kitty.Options{Action: kitty.Delete, ID: p.imageID, Delete: kitty.DeleteID, DeleteResources: resources, Quiet: 2}
	return p.wrap(ansi.KittyGraphics(nil, o.Options()...))
}

// SetPNG decodes and encodes outside the output mutex. Call from background
// work for large images; a new image appears on the next host frame.
func (p *Presenter) SetPNG(data []byte) error {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if format != "png" {
		return errors.New("preview artifact must be PNG")
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 64_000_000 {
		return errors.New("PNG exceeds the preview pixel limit")
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var encoded bytes.Buffer
	opts := kitty.Options{Action: kitty.Transmit, ID: p.imageID, Format: kitty.PNG, Transmission: kitty.Direct, Chunk: true, Quiet: 2}
	if p.tmux {
		opts.ChunkFormatter = ansi.TmuxPassthrough
	}
	if err := kitty.EncodeGraphics(&encoded, im, &opts); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("graphics presenter is closed")
	}
	p.encoded = encoded.String()
	p.bounds = im.Bounds()
	p.dirty = true
	p.zoom = 1
	p.panX = 0
	p.panY = 0
	return nil
}
func (p *Presenter) Configure(rect Rect, visible bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rect != rect {
		if w, h := terminalCellSize(p.out); w > 0 && h > 0 {
			p.cellWidth, p.cellHeight, p.cellKnown = w, h, true
		}
	}
	p.rect = rect
	p.visible = visible
	p.clampPan()
}
func (p *Presenter) CellSize(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cellWidth, p.cellHeight, p.cellKnown = w, h, true
	p.refreshControl()
}
func (p *Presenter) Zoom(factor float64) {
	if factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.zoom = max(1, min(8, p.zoom*factor))
	p.clampPan()
	p.refreshControl()
}
func (p *Presenter) Fit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.zoom = 1
	p.panX = 0
	p.panY = 0
	p.refreshControl()
}
func (p *Presenter) Pan(x, y int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, h := p.crop()
	// Clamp every step so panning against an edge never accumulates hidden motion.
	p.panX = int(max(0, min(float64(max(0, p.bounds.Dx()-w)), float64(p.panX)+float64(x)*20)))
	p.panY = int(max(0, min(float64(max(0, p.bounds.Dy()-h)), float64(p.panY)+float64(y)*20)))
	p.refreshControl()
}
func (p *Presenter) crop() (int, int) {
	iw, ih := p.bounds.Dx(), p.bounds.Dy()
	if iw <= 0 || ih <= 0 || p.rect.Width <= 0 || p.rect.Height <= 0 {
		return 0, 0
	}
	if p.zoom == 1 {
		return iw, ih
	}
	pw, ph := float64(p.rect.Width)*float64(p.cellWidth), float64(p.rect.Height)*float64(p.cellHeight)
	scale := math.Min(pw/float64(iw), ph/float64(ih)) * p.zoom
	return max(1, min(iw, int(pw/scale))), max(1, min(ih, int(ph/scale)))
}
func (p *Presenter) clampPan() {
	w, h := p.crop()
	p.panX = max(0, min(max(0, p.bounds.Dx()-w), p.panX))
	p.panY = max(0, min(max(0, p.bounds.Dy()-h), p.panY))
}
func (p *Presenter) placement() string {
	if !p.visible || !p.terminalActive || p.rect.X < 0 || p.rect.Y < 0 || p.rect.Width < 1 || p.rect.Height < 1 || p.encoded == "" {
		return ""
	}
	w, h := p.crop()
	if w < 1 || h < 1 {
		return ""
	}
	p.clampPan()
	pw, ph := float64(p.rect.Width)*float64(p.cellWidth), float64(p.rect.Height)*float64(p.cellHeight)
	scale := math.Min(pw/float64(w), ph/float64(h))
	opts := kitty.Options{Action: kitty.Put, ID: p.imageID, PlacementID: 1, Width: w, Height: h, X: p.panX, Y: p.panY, DoNotMoveCursor: true, Quiet: 2}
	if p.cellKnown {
		// Let the terminal compute the other axis, preserving pixel aspect.
		if pw/float64(w) <= ph/float64(h) {
			opts.Columns = p.rect.Width
		} else {
			opts.Rows = p.rect.Height
		}
	} else {
		// SSH can omit pixel dimensions. Bound both axes using the 8x16 estimate;
		// containment is more important than an unsupported aspect guarantee.
		opts.Columns = max(1, min(p.rect.Width, int(math.Ceil(float64(w)*scale/float64(p.cellWidth)))))
		opts.Rows = max(1, min(p.rect.Height, int(math.Ceil(float64(h)*scale/float64(p.cellHeight)))))
	}
	return ansi.SaveCursor + ansi.CursorPosition(p.rect.X+1, p.rect.Y+1) + p.wrap(ansi.KittyGraphics(nil, opts.Options()...)) + ansi.RestoreCursor
}
func (p *Presenter) observeCSI(cmd ansi.Cmd, params ansi.Params) {
	if cmd.Prefix() == 0 && cmd.Intermediate() == 0 && cmd.Final() == 'J' {
		mode, _, _ := params.Param(0, 0)
		if mode == 2 || mode == 3 {
			p.dirty = true
			p.uploaded = false
			p.placed = false
		}
	}
	if cmd.Prefix() == '?' && cmd.Intermediate() == 0 && (cmd.Final() == 'h' || cmd.Final() == 'l') {
		params.ForEach(0, func(_ int, value int, _ bool) {
			if value == 47 || value == 1047 || value == 1049 {
				p.terminalActive = cmd.Final() == 'h'
				p.dirty = true
				if p.terminalActive {
					p.uploaded = false
					p.placed = false
				}
			}
		})
	}
}
func (p *Presenter) observeESC(cmd ansi.Cmd) {
	if cmd.Intermediate() == 0 && cmd.Final() == 'c' {
		p.dirty = true
		p.uploaded = false
		p.placed = false
		p.terminalActive = false
	}
}

// Write appends graphics after a framework frame in the same underlying write.
// The upstream streaming parser avoids inserting graphics in a partial escape
// sequence and recognizes clear-screen and alternate-screen transitions.
func (p *Presenter) Write(frame []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	for _, b := range frame {
		p.stream.Advance(b)
	}
	return p.flush(frame)
}
func (p *Presenter) refreshControl() {
	// Bubble Tea skips frames whose text did not change. Refresh existing camera
	// placements under the same mutex; never transmit a new image before its frame.
	if p.placed && !p.dirty && !p.closed && p.writeErr == nil {
		_, _ = p.flush(nil)
	}
}
func (p *Presenter) flush(frame []byte) (int, error) {
	var tail strings.Builder
	uploaded, placed, dirty := p.uploaded, p.placed, p.dirty
	if !p.closed && p.encoded != "" && p.stream.State() == parser.GroundState {
		placement := p.placement()
		if placement == "" {
			if placed || (!p.terminalActive && uploaded) {
				tail.WriteString(p.delete(!p.terminalActive))
				placed = false
				if !p.terminalActive {
					uploaded = false
					dirty = true
				}
			}
		} else {
			if placed {
				tail.WriteString(p.delete(false))
			}
			if dirty || !uploaded {
				tail.WriteString(p.encoded)
				uploaded = true
				dirty = false
			}
			tail.WriteString(placement)
			placed = true
		}
	}
	if len(frame) == 0 && tail.Len() == 0 {
		return 0, nil
	}
	output := make([]byte, 0, len(frame)+tail.Len())
	output = append(output, frame...)
	output = append(output, tail.String()...)
	n, err := p.out.Write(output)
	if n < 0 || n > len(output) {
		n = 0
		if err == nil {
			err = errors.New("invalid terminal write count")
		}
	}
	if err == nil && n != len(output) {
		err = io.ErrShortWrite
	}
	if err != nil {
		p.writeErr = err
		return min(n, len(frame)), err
	}
	p.uploaded, p.placed, p.dirty = uploaded, placed, dirty
	return len(frame), nil
}
func (p *Presenter) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.writeErr
	}
	p.closed = true
	p.visible = false
	if p.writeErr != nil {
		return p.writeErr
	}
	if !p.uploaded && !p.placed {
		return nil
	}
	if p.stream.State() != parser.GroundState {
		return errors.New("terminal stream ended inside an escape sequence; image cleanup could not be sent safely")
	}
	sequence := p.delete(true)
	n, err := io.WriteString(p.out, sequence)
	if err == nil && n != len(sequence) {
		err = io.ErrShortWrite
	}
	if err != nil {
		p.writeErr = err
		return err
	}
	p.uploaded = false
	p.placed = false
	return nil
}
