package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"lazymermaid/internal/document"
)

func isolatedCLI(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, leaf := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state"} {
		t.Setenv(name, filepath.Join(root, leaf))
	}
	for _, name := range []string{"LAZYMERMAID_NVIM", "LAZYMERMAID_NODE", "LAZYMERMAID_RUNTIME_DIR", "LAZYMERMAID_TERMAID", "LAZYMERMAID_PREVIEW", "LAZYMERMAID_THEME"} {
		// Setenv registers restoration; the unset below prevents a caller's real
		// environment from overriding a test's explicit TOML configuration.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func executeCLI(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	command := NewCommand("v0.0.0-test")
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(stdin))
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func writeCLIFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func decodeCLIJSON(t *testing.T, stdout string, value any) {
	t.Helper()
	if strings.ContainsRune(stdout, '\x1b') {
		t.Fatalf("ANSI escaped into machine output: %q", stdout)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected output after JSON: %q, %v", stdout, err)
	}
}

func TestCLIScanJSONIncludesSourceIdentityAndRanges(t *testing.T) {
	root := isolatedCLI(t)
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(sources, 0755); err != nil {
		t.Fatal(err)
	}
	markdown := "# 系統\n\n```mermaid\nflowchart LR\n甲-->乙\n```\n\n> ```mermaid\n> sequenceDiagram\n> A->>B: hi\n> ```\n"
	writeCLIFile(t, filepath.Join(sources, "guide.md"), markdown, 0644)
	writeCLIFile(t, filepath.Join(sources, "standalone.mmd"), "flowchart TD\nA-->B\n", 0644)
	stdout, stderr, err := executeCLI(t, "", "scan", sources, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("scan failed: %v, stderr=%q", err, stderr)
	}
	var output struct {
		Diagrams []document.Diagram   `json:"diagrams"`
		Issues   []document.ScanIssue `json:"issues"`
	}
	decodeCLIJSON(t, stdout, &output)
	if len(output.Diagrams) != 3 || len(output.Issues) != 0 {
		t.Fatalf("wrong discovery result: %+v", output)
	}
	first, nested, standalone := output.Diagrams[0], output.Diagrams[1], output.Diagrams[2]
	if first.Title != "系統" || first.StartLine != 3 || first.EndLine != 6 || first.Source != "flowchart LR\n甲-->乙\n" || !first.VirtualEditable {
		t.Fatalf("top-level source lost its context: %+v", first)
	}
	if nested.TopLevel || nested.VirtualEditable || nested.Source != "sequenceDiagram\nA->>B: hi\n" {
		t.Fatalf("nested source was not extracted correctly: %+v", nested)
	}
	if !standalone.Standalone || standalone.Path != filepath.Join(sources, "standalone.mmd") {
		t.Fatalf("standalone source not identified: %+v", standalone)
	}
	for _, d := range output.Diagrams {
		if !filepath.IsAbs(d.Path) || d.ID == "" || len(d.DocumentHash) != 64 {
			t.Errorf("source identity missing: %+v", d)
		}
	}
}

func TestCLIUsageErrorsDoNotStartRendering(t *testing.T) {
	root := isolatedCLI(t)
	t.Setenv("LAZYMERMAID_NODE", filepath.Join(root, "unavailable-node"))
	t.Setenv("LAZYMERMAID_TERMAID", filepath.Join(root, "unavailable-termaid"))
	missing := filepath.Join(root, "missing.md")
	badConfig := filepath.Join(root, "bad.toml")
	writeCLIFile(t, badConfig, "[preview\n", 0644)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"bad format", []string{"render", "-", "--format", "gif"}},
		{"missing input argument", []string{"render", "--format", "ascii"}},
		{"missing source file", []string{"render", missing, "--format", "ascii"}},
		{"missing explicit config", []string{"--config", filepath.Join(root, "missing.toml"), "scan", root, "--json"}},
		{"malformed config", []string{"--config", badConfig, "scan", root, "--json"}},
		{"invalid line", []string{"render", "-", "--line", "-1"}},
		{"explicit zero line", []string{"render", "-", "--line", "0"}},
		{"unknown flag", []string{"render", "-", "--fomat", "ascii"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := executeCLI(t, "graph TD\n", tt.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("expected usage exit 2, got %v (code %d)", err, ExitCode(err))
			}
			if stdout != "" {
				t.Errorf("usage failure polluted stdout: %q", stdout)
			}
		})
	}
}

func TestCLIHelpAndVersionBypassBrokenConfiguration(t *testing.T) {
	root := isolatedCLI(t)
	missing := filepath.Join(root, "does-not-exist.toml")
	badConfig := filepath.Join(root, "broken.toml")
	writeCLIFile(t, badConfig, "[malformed\n", 0644)
	for _, path := range []string{missing, badConfig} {
		for _, flag := range []string{"--help", "--version"} {
			stdout, stderr, err := executeCLI(t, "", "--config", path, flag)
			if err != nil || stderr != "" {
				t.Fatalf("%s depended on config %s: %v, %q", flag, path, err, stderr)
			}
			if flag == "--version" && stdout != "lazymermaid v0.0.0-test\n" {
				t.Errorf("wrong version output: %q", stdout)
			}
			if flag == "--help" && !strings.Contains(stdout, "scan") {
				t.Errorf("help missing command tree: %q", stdout)
			}
		}
	}
	for _, leaf := range []string{"config", "data", "cache", "state"} {
		if _, err := os.Stat(filepath.Join(root, leaf)); !os.IsNotExist(err) {
			t.Errorf("static path created %s: %v", leaf, err)
		}
	}
}

func TestCLIDoctorJSONWithoutInstalledBackends(t *testing.T) {
	root := isolatedCLI(t)
	missing := filepath.Join(root, "unavailable-backend")
	configPath := filepath.Join(root, "config.toml")
	contents := fmt.Sprintf("[editor]\ncommand = %q\n[render]\nnode = %q\ntermaid = %q\nruntime_dir = %q\n", missing, missing, missing, filepath.Join(root, "missing-runtime"))
	writeCLIFile(t, configPath, contents, 0644)
	stdout, stderr, err := executeCLI(t, "", "--config", configPath, "doctor", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("optional dependencies blocked doctor: %v, %q", err, stderr)
	}
	var capabilities []Capability
	decodeCLIJSON(t, stdout, &capabilities)
	byName := make(map[string]Capability)
	for _, capability := range capabilities {
		byName[capability.Name] = capability
	}
	for _, name := range []string{"Neovim", "Node", "termaid", "Mermaid runtime"} {
		entry, ok := byName[name]
		if !ok || entry.Status != "optional missing" || entry.Hint == "" {
			t.Errorf("missing actionable capability %s: %+v", name, entry)
		}
	}
}

func fakeTermaidConfig(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this CLI subprocess fixture uses the supported macOS/Linux shell boundary")
	}
	command := filepath.Join(root, "fake-termaid")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  printf '%s\\n' 'termaid test-fixture'\n  exit 0\nfi\n[ \"$1\" = \"--no-auto-fit\" ] || exit 21\n[ \"$2\" = \"--ascii\" ] || exit 22\n[ \"$NO_COLOR\" = \"1\" ] || exit 23\nprintf '%s\\n' 'ASCII fixture:'\ncat\n"
	writeCLIFile(t, command, script, 0700)
	path := filepath.Join(root, "render.toml")
	writeCLIFile(t, path, fmt.Sprintf("[render]\ntermaid = %q\nnode = %q\n", command, filepath.Join(root, "no-node")), 0644)
	return path
}

func TestCLIRenderStdinUsesConfiguredTextBackend(t *testing.T) {
	root := isolatedCLI(t)
	configPath := fakeTermaidConfig(t, root)
	source := "flowchart LR\n甲[\"literal $(text)\"] --> 乙\n"
	stdout, stderr, err := executeCLI(t, source, "--config", configPath, "render", "-", "--format", "ascii")
	if err != nil || stderr != "" {
		t.Fatalf("stdin render failed: %v, stderr=%q", err, stderr)
	}
	if want := "ASCII fixture:\n" + source; stdout != want {
		t.Fatalf("source was transformed or output polluted: got %q, want %q", stdout, want)
	}
	outputPath := filepath.Join(root, "output.txt")
	stdout, stderr, err = executeCLI(t, source, "--config", configPath, "render", "-", "--format", "ascii", "--output", outputPath)
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("file render polluted streams: %q, %q, %v", stdout, stderr, err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil || string(data) != "ASCII fixture:\n"+source {
		t.Fatalf("wrong saved output: %q, %v", data, err)
	}
}

func TestCLIRenderMarkdownRequiresUnambiguousLineSelection(t *testing.T) {
	root := isolatedCLI(t)
	configPath := fakeTermaidConfig(t, root)
	path := filepath.Join(root, "multiple.md")
	writeCLIFile(t, path, "# Examples\n\n```mermaid\nflowchart LR\nfirst-->one\n```\n\n```mermaid\nflowchart TD\nsecond-->two\n```\n", 0644)
	stdout, _, err := executeCLI(t, "", "--config", configPath, "render", path, "--format", "ascii")
	if err == nil || ExitCode(err) != 2 || !errors.Is(err, document.ErrAmbiguous) || stdout != "" {
		t.Fatalf("ambiguous source selected implicitly: %q, %v", stdout, err)
	}
	stdout, stderr, err := executeCLI(t, "", "--config", configPath, "render", path, "--line", "9", "--format", "ascii")
	if err != nil || stderr != "" || stdout != "ASCII fixture:\nflowchart TD\nsecond-->two\n" {
		t.Fatalf("line selection rendered the wrong diagram: %q, %q, %v", stdout, stderr, err)
	}
	stdout, _, err = executeCLI(t, "", "--config", configPath, "render", path, "--line", "7", "--format", "ascii")
	if !errors.Is(err, document.ErrNoDiagram) || ExitCode(err) != 2 || stdout != "" {
		t.Fatalf("outside-block line chose a nearby diagram: %q, %v", stdout, err)
	}
}
