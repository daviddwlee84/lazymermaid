// Package render delegates Mermaid syntax, layout, and output to upstream tools.
package render

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MermaidVersion   = "12.0.0"
	PuppeteerVersion = "25.12.0"
	maxResponseBytes = 64 << 20
)

//go:embed worker.mjs
var workerScript []byte

type Options struct {
	Node, RuntimeDir, Termaid, CacheDir string
	Timeout                             time.Duration
}

type Request struct {
	Source string          `json:"source"`
	Format string          `json:"format"`
	Theme  string          `json:"theme,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Line and Column are one-based upstream coordinates. They must not be mapped
// into an editor unless Reliable is true; Mermaid preprocessing can move them.
type Diagnostic struct {
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Reliable bool   `json:"reliable"`
}

type Info struct {
	Backend          string `json:"backend"`
	Version          string `json:"version"`
	MermaidVersion   string `json:"mermaidVersion,omitempty"`
	PuppeteerVersion string `json:"puppeteerVersion,omitempty"`
	BrowserVersion   string `json:"browserVersion,omitempty"`
}

type Result struct {
	Data        []byte       `json:"data,omitempty"`
	Format      string       `json:"format,omitempty"`
	DiagramType string       `json:"diagramType,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Backend     Info         `json:"backend"`
}

type ErrorKind string

const (
	Unavailable ErrorKind = "unavailable"
	Runtime     ErrorKind = "runtime"
	Invalid     ErrorKind = "invalid"
	Timeout     ErrorKind = "timeout"
)

type Error struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }
func IsUnavailable(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Kind == Unavailable
}

type workerRequest struct {
	ID        uint64 `json:"id"`
	Operation string `json:"operation"`
	Request
}

type workerResponse struct {
	ID      uint64    `json:"id"`
	OK      bool      `json:"ok"`
	Kind    ErrorKind `json:"kind,omitempty"`
	Message string    `json:"message,omitempty"`
	Result
}

type process struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	responses chan workerResponse
	done      chan error
	stderr    *tailBuffer
}

type Service struct {
	opts     Options
	gate     chan struct{}
	proc     *process
	sequence uint64
	info     Info
	textInfo Info
	closed   bool
}

func New(opts Options) *Service {
	if opts.Node == "" {
		opts.Node = "node"
	}
	if opts.Termaid == "" {
		opts.Termaid = "termaid"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.RuntimeDir == "" {
		opts.RuntimeDir = DefaultRuntimeDir()
	}
	if opts.CacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		opts.CacheDir = filepath.Join(base, "lazymermaid", "render")
	}
	service := &Service{opts: opts, gate: make(chan struct{}, 1)}
	service.gate <- struct{}{}
	return service
}

func DefaultRuntimeDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "lazymermaid", "runtime", "mermaid")
}

func (s *Service) RuntimeDir() string { return s.opts.RuntimeDir }

func (s *Service) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
		if err := ctx.Err(); err != nil {
			s.gate <- struct{}{}
			return err
		}
		if s.closed {
			s.gate <- struct{}{}
			return &Error{Kind: Runtime, Message: "renderer service is closed"}
		}
		return nil
	}
}
func (s *Service) unlock() { s.gate <- struct{}{} }

func (s *Service) Info(ctx context.Context) (Info, error) {
	if err := s.lock(ctx); err != nil {
		return Info{}, err
	}
	defer s.unlock()
	return s.infoLocked(ctx)
}

func (s *Service) infoLocked(ctx context.Context) (Info, error) {
	if s.proc != nil {
		select {
		case <-s.proc.done:
			s.stopLocked()
		default:
		}
	}
	if s.proc != nil && s.info.MermaidVersion != "" {
		return s.info, nil
	}
	response, err := s.exchangeLocked(ctx, "info", Request{})
	if err != nil {
		return Info{}, err
	}
	s.info = response.Backend
	return s.info, nil
}

// TextInfo reports the independent text renderer; it does not require Node.
func (s *Service) TextInfo(ctx context.Context) (Info, error) {
	if err := s.lock(ctx); err != nil {
		return Info{}, err
	}
	defer s.unlock()
	return s.textInfoLocked(ctx)
}

func (s *Service) textInfoLocked(ctx context.Context) (Info, error) {
	if s.textInfo.Version != "" {
		return s.textInfo, nil
	}
	data, err := s.runTextLocked(ctx, "", "--version")
	if err != nil {
		return Info{}, err
	}
	s.textInfo = Info{Backend: "termaid", Version: strings.TrimSpace(string(data))}
	return s.textInfo, nil
}

func (s *Service) Validate(ctx context.Context, source string) (Result, error) {
	if err := s.lock(ctx); err != nil {
		return Result{}, err
	}
	defer s.unlock()
	response, err := s.exchangeLocked(ctx, "validate", Request{Source: source})
	return response.Result, err
}

func (s *Service) Render(ctx context.Context, request Request) (Result, error) {
	if request.Format == "" {
		request.Format = "svg"
	}
	if len(request.Config) > 0 {
		var config map[string]json.RawMessage
		if err := json.Unmarshal(request.Config, &config); err != nil || config == nil {
			return Result{}, &Error{Kind: Invalid, Message: "Mermaid configuration must be a JSON object", Cause: err}
		}
	}
	if !oneOf(request.Format, "svg", "png", "unicode", "ascii") {
		return Result{}, &Error{Kind: Invalid, Message: "output format must be svg, png, unicode, or ascii"}
	}
	if err := s.lock(ctx); err != nil {
		return Result{}, err
	}
	defer s.unlock()
	var backend Info
	var err error
	text := request.Format == "unicode" || request.Format == "ascii"
	if text {
		backend, err = s.textInfoLocked(ctx)
	} else {
		backend, err = s.infoLocked(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	key := cacheKey(request, backend)
	if cached, ok := s.readCache(key); ok {
		return cached, nil
	}
	var result Result
	if text {
		args := []string{"--no-auto-fit"}
		if request.Format == "ascii" {
			args = append(args, "--ascii")
		}
		data, renderErr := s.runTextLocked(ctx, request.Source, args...)
		result = Result{Data: data, Format: request.Format, Backend: backend}
		if renderErr != nil {
			result.Diagnostics = []Diagnostic{{Message: renderErr.Error()}}
			return result, renderErr
		}
	} else {
		response, renderErr := s.exchangeLocked(ctx, "render", request)
		result = response.Result
		if renderErr != nil {
			return result, renderErr
		}
	}
	s.writeCache(key, result)
	return result, nil
}

func (s *Service) startLocked() error {
	if s.proc != nil {
		return nil
	}
	node, err := exec.LookPath(s.opts.Node)
	if err != nil {
		return &Error{Kind: Unavailable, Message: "Node.js is unavailable: " + err.Error(), Cause: err}
	}
	dir := filepath.Join(s.opts.CacheDir, "bridge")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &Error{Kind: Runtime, Message: "create renderer bridge directory: " + err.Error(), Cause: err}
	}
	digest := sha256.Sum256(workerScript)
	filename := filepath.Join(dir, "worker-"+hex.EncodeToString(digest[:8])+".mjs")
	if existing, err := os.ReadFile(filename); err != nil || !bytes.Equal(existing, workerScript) {
		if err := atomicWrite(filename, workerScript); err != nil {
			return &Error{Kind: Runtime, Message: "write renderer bridge: " + err.Error(), Cause: err}
		}
	}
	command := exec.Command(node, filename, s.opts.RuntimeDir)
	configureProcess(command)
	stderr := &tailBuffer{limit: 16 << 10}
	command.Stderr = stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return &Error{Kind: Unavailable, Message: "start official renderer: " + err.Error(), Cause: err}
	}
	p := &process{command: command, stdin: stdin, responses: make(chan workerResponse, 1), done: make(chan error, 1), stderr: stderr}
	s.proc = p
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
		var scanErr error
		for scanner.Scan() {
			var response workerResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				scanErr = fmt.Errorf("invalid worker response: %w", err)
				break
			}
			select {
			case p.responses <- response:
			default:
				scanErr = errors.New("unexpected unsolicited worker response")
			}
			if scanErr != nil {
				break
			}
		}
		if scanErr == nil {
			scanErr = scanner.Err()
		}
		if scanErr != nil {
			_ = killProcess(command)
		}
		waitErr := command.Wait()
		if scanErr != nil {
			waitErr = scanErr
		}
		p.done <- waitErr
		close(p.done)
	}()
	return nil
}

func (s *Service) exchangeLocked(ctx context.Context, operation string, request Request) (workerResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return workerResponse{}, err
	}
	if err := s.startLocked(); err != nil {
		return workerResponse{}, err
	}
	s.sequence++
	message, err := json.Marshal(workerRequest{ID: s.sequence, Operation: operation, Request: request})
	if err != nil {
		return workerResponse{}, &Error{Kind: Invalid, Message: err.Error(), Cause: err}
	}
	p := s.proc
	written := make(chan error, 1)
	go func() { _, err := p.stdin.Write(append(message, '\n')); written <- err }()
	select {
	case err = <-written:
		if err != nil {
			s.stopLocked()
			return workerResponse{}, &Error{Kind: Runtime, Message: "write to official renderer: " + err.Error(), Cause: err}
		}
	case <-ctx.Done():
		s.stopLocked()
		return workerResponse{}, contextError(ctx.Err())
	}
	select {
	case response := <-p.responses:
		if response.ID != s.sequence {
			s.stopLocked()
			return workerResponse{}, &Error{Kind: Runtime, Message: "official renderer returned a mismatched request ID"}
		}
		if !response.OK {
			kind := response.Kind
			if kind == "" {
				kind = Runtime
			}
			if kind == Unavailable || kind == Runtime {
				s.stopLocked()
			}
			return response, &Error{Kind: kind, Message: response.Message}
		}
		return response, nil
	case err := <-p.done:
		message := "official renderer exited unexpectedly"
		if err != nil {
			message += ": " + err.Error()
		}
		if tail := p.stderr.String(); tail != "" {
			message += "\n" + tail
		}
		s.stopLocked()
		return workerResponse{}, &Error{Kind: Runtime, Message: message, Cause: err}
	case <-ctx.Done():
		s.stopLocked()
		return workerResponse{}, contextError(ctx.Err())
	}
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: Timeout, Message: "renderer request timed out", Cause: err}
	}
	return err
}

func (s *Service) runTextLocked(ctx context.Context, source string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(s.opts.Termaid)
	if err != nil {
		return nil, &Error{Kind: Unavailable, Message: "termaid is unavailable; install the upstream termaid CLI: " + err.Error(), Cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	configureProcess(command)
	command.Cancel = func() error { return killProcess(command) }
	command.WaitDelay = time.Second
	command.Stdin = strings.NewReader(source)
	command.Env = append(os.Environ(), "NO_COLOR=1")
	output := &limitedBuffer{limit: maxResponseBytes}
	stderr := &tailBuffer{limit: 16 << 10}
	command.Stdout, command.Stderr = output, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, contextError(ctx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		// A text renderer failure is not an official Mermaid syntax verdict.
		return nil, &Error{Kind: Runtime, Message: "termaid: " + message, Cause: err}
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func (s *Service) stopLocked() {
	p := s.proc
	s.proc, s.info = nil, Info{}
	if p == nil {
		return
	}
	_ = p.stdin.Close()
	_ = terminateProcess(p.command)
	select {
	case <-p.done:
	case <-time.After(800 * time.Millisecond):
		_ = killProcess(p.command)
		select {
		case <-p.done:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Service) Close() error {
	<-s.gate
	defer s.unlock()
	if !s.closed {
		s.stopLocked()
		s.closed = true
	}
	return nil
}

func cacheKey(request Request, backend Info) string {
	data, _ := json.Marshal(struct {
		Request Request
		Backend Info
		Schema  int
	}{request, backend, 1})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Service) readCache(key string) (Result, bool) {
	filename := filepath.Join(s.opts.CacheDir, "artifacts", key+".json")
	stat, err := os.Stat(filename)
	if err != nil || stat.Size() > maxResponseBytes {
		return Result{}, false
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return Result{}, false
	}
	var result Result
	if json.Unmarshal(data, &result) != nil || len(result.Data) == 0 {
		return Result{}, false
	}
	return result, true
}

func (s *Service) writeCache(key string, result Result) {
	if len(result.Data) == 0 {
		return
	}
	dir := filepath.Join(s.opts.CacheDir, "artifacts")
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	data, err := json.Marshal(result)
	if err == nil {
		_ = atomicWrite(filepath.Join(dir, key+".json"), data)
	}
}

func atomicWrite(filename string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".lazymermaid-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filename)
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if choice == value {
			return true
		}
	}
	return false
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	b.data = append(b.data, data...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return n, nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

// Override bytes.Buffer.ReadFrom as well: os/exec uses io.Copy, whose fast path
// would otherwise bypass the output limit implemented by Write.
func (b *limitedBuffer) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, reader)
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		return 0, errors.New("renderer output exceeds limit")
	}
	return b.Buffer.Write(data)
}
