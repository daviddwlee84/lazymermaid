package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake process tests the real stdio transport, failure recovery, and cache
// boundary without requiring Node or a browser in a normal go test run.
func fakeWorker(t *testing.T) (string, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("transport fixture requires python3")
	}
	dir := t.TempDir()
	script := `#!` + python + `
import base64,json,pathlib,sys,time
root=pathlib.Path(sys.argv[2])
version=(root/'version').read_text().strip()
backend={'backend':'mermaid','version':'fixture','mermaidVersion':version}
for line in sys.stdin:
 r=json.loads(line)
 if r.get('source')=='hang': time.sleep(10)
 if r.get('source')=='crash': sys.exit(7)
 out={'id':r['id'],'ok':True,'backend':backend}
 if r['operation']=='render':
  count=root/'count'; n=int(count.read_text())+1 if count.exists() else 1; count.write_text(str(n))
  out.update(format=r['format'],diagramType='flowchart',data=base64.b64encode(('render-'+str(n)).encode()).decode())
 print(json.dumps(out),flush=True)
`
	filename := filepath.Join(dir, "fake node")
	if err := os.WriteFile(filename, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte("12.0.0"), 0600); err != nil {
		t.Fatal(err)
	}
	return filename, dir
}

func TestCacheIncludesSourceOptionsAndEffectiveEngine(t *testing.T) {
	node, runtime := fakeWorker(t)
	cache := t.TempDir()
	service := New(Options{Node: node, RuntimeDir: runtime, CacheDir: cache})
	request := Request{Source: "flowchart TD\n A-->B", Format: "svg"}
	first, err := service.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Render(context.Background(), request)
	if err != nil || string(second.Data) != string(first.Data) {
		t.Fatalf("cache: %q, %v", second.Data, err)
	}
	request.Theme = "dark"
	third, err := service.Render(context.Background(), request)
	if err != nil || string(third.Data) == string(first.Data) {
		t.Fatalf("theme must invalidate cache: %q, %v", third.Data, err)
	}
	_ = service.Close()
	if err := os.WriteFile(filepath.Join(runtime, "version"), []byte("12.1.0"), 0600); err != nil {
		t.Fatal(err)
	}
	service = New(Options{Node: node, RuntimeDir: runtime, CacheDir: cache})
	defer service.Close()
	fourth, err := service.Render(context.Background(), request)
	if err != nil || string(fourth.Data) == string(third.Data) {
		t.Fatalf("engine must invalidate cache: %q, %v", fourth.Data, err)
	}
}

func TestTimeoutAndCrashRestartWorker(t *testing.T) {
	node, runtime := fakeWorker(t)
	service := New(Options{Node: node, RuntimeDir: runtime, CacheDir: t.TempDir(), Timeout: time.Second})
	defer service.Close()
	if _, err := service.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := service.Validate(ctx, "hang")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != Timeout {
		t.Fatalf("want timeout, got %v", err)
	}
	if _, err := service.Validate(context.Background(), "ok"); err != nil {
		t.Fatalf("restart after timeout: %v", err)
	}
	if _, err := service.Validate(context.Background(), "crash"); err == nil {
		t.Fatal("expected worker exit error")
	}
	if _, err := service.Validate(context.Background(), "ok"); err != nil {
		t.Fatalf("restart after exit: %v", err)
	}
}

func TestUnavailableAndConfigurationErrors(t *testing.T) {
	service := New(Options{Node: filepath.Join(t.TempDir(), "absent"), CacheDir: t.TempDir()})
	defer service.Close()
	if _, err := service.Info(context.Background()); !IsUnavailable(err) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	for _, config := range []json.RawMessage{json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`{"broken"`)} {
		_, err := service.Render(context.Background(), Request{Config: config, Format: "svg"})
		var failure *Error
		if !errors.As(err, &failure) || failure.Kind != Invalid {
			t.Fatalf("expected config error for %s: %v", config, err)
		}
	}
}

func TestTextRendererReceivesRawSourceAndFlags(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "termaid")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'termaid fixture'; exit 0; fi\nprintf '%s\\n' \"$@\"\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Termaid: file, Node: "definitely-not-installed", CacheDir: t.TempDir()})
	defer service.Close()
	source := "flowchart LR\n A[中文] --> B\n"
	result, err := service.Render(context.Background(), Request{Source: source, Format: "ascii"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "--no-auto-fit\n--ascii\n"+source {
		t.Fatalf("unexpected source/flags: %q", result.Data)
	}
	if result.Backend.Backend != "termaid" {
		t.Fatalf("wrong backend: %+v", result.Backend)
	}
}

func TestOutputLimitCannotBeBypassedByReaderFrom(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	_, err := buffer.ReadFrom(strings.NewReader("too much data"))
	if err == nil || buffer.Len() > 4 {
		t.Fatalf("output limit bypassed: %d, %v", buffer.Len(), err)
	}
}

func TestOfficialRuntime(t *testing.T) {
	runtime := os.Getenv("LAZYMERMAID_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("set LAZYMERMAID_TEST_RUNTIME to an installed runtime/mermaid directory")
	}
	service := New(Options{RuntimeDir: runtime, CacheDir: t.TempDir(), Timeout: 30 * time.Second})
	defer service.Close()
	info, err := service.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.MermaidVersion != MermaidVersion || info.BrowserVersion == "" {
		t.Fatalf("missing effective engine info: %+v", info)
	}
	source := "---\ntitle: Test\n---\nflowchart TD\n %% comment\n A[中文] --> B[World]"
	valid, err := service.Validate(context.Background(), source)
	if err != nil || valid.DiagramType == "" {
		t.Fatalf("official validation: %+v %v", valid, err)
	}
	invalid, err := service.Validate(context.Background(), "flowchart TD\n A[broken")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != Invalid || len(invalid.Diagnostics) == 0 {
		t.Fatalf("invalid syntax: %+v %v", invalid, err)
	}
	if invalid.Diagnostics[0].Reliable {
		t.Fatal("upstream parser coordinates must not be represented as reliable source locations")
	}
	for _, format := range []string{"svg", "png"} {
		result, err := service.Render(context.Background(), Request{Source: source, Format: format, Theme: "dark"})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if format == "svg" && !bytes.Contains(result.Data, []byte("<svg")) {
			t.Fatalf("missing SVG: %q", result.Data)
		}
		if format == "png" {
			if _, err := png.DecodeConfig(bytes.NewReader(result.Data)); err != nil {
				t.Fatalf("invalid PNG: %v", err)
			}
		}
	}
}
