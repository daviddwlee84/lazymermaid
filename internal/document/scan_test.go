package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestScanHonorsIgnoreAndHiddenPolicies(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"visible.md", "second.mermaid", "nested/a.markdown", "UPPER.MD", "ignored.mmd", "ignored-dir/x.mmd", "filtered.md", ".hidden.mmd", ".hidden-dir/a.md", ".git/objects/not-source.mmd"} {
		source := "graph TD\nA-->B\n"
		if ext := strings.ToLower(filepath.Ext(name)); ext == ".md" || ext == ".markdown" {
			source = "```mermaid\n" + source + "```\n"
		}
		write(name, source)
	}
	write(".gitignore", "ignored*\n")
	write(".ignore", "filtered.md\n")
	write("empty.md", "# no diagrams\n")
	write("bad.md", string([]byte{0xff}))
	write("other.txt", "```mermaid\ngraph TD\n```\n")

	for _, tt := range []struct {
		name string
		opts ScanOptions
		want []string
	}{
		{"default", ScanOptions{}, []string{"UPPER.MD", "nested/a.markdown", "second.mermaid", "visible.md"}},
		{"hidden", ScanOptions{Hidden: true}, []string{".hidden-dir/a.md", ".hidden.mmd", "UPPER.MD", "nested/a.markdown", "second.mermaid", "visible.md"}},
		{"all", ScanOptions{Hidden: true, NoIgnore: true}, []string{".hidden-dir/a.md", ".hidden.mmd", "UPPER.MD", "filtered.md", "ignored-dir/x.mmd", "ignored.mmd", "nested/a.markdown", "second.mermaid", "visible.md"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			diagrams, issues, err := Scan(context.Background(), root, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, d := range diagrams {
				if !filepath.IsAbs(d.Path) {
					t.Errorf("non-absolute path %q", d.Path)
				}
				rel, _ := filepath.Rel(root, d.Path)
				names = append(names, filepath.ToSlash(rel))
			}
			sort.Strings(tt.want)
			if !reflect.DeepEqual(names, tt.want) {
				t.Errorf("got %v; want %v", names, tt.want)
			}
			if len(issues) != 1 || issues[0].Path != filepath.Join(root, "bad.md") {
				t.Errorf("expected bad UTF-8 issue while preserving good rows: %+v", issues)
			}
		})
	}

	diagrams, issues, err := Scan(context.Background(), filepath.Join(root, ".hidden.mmd"), ScanOptions{})
	if err != nil || len(diagrams) != 1 || len(issues) != 0 {
		t.Fatalf("explicit hidden file was filtered: %+v, %+v, %v", diagrams, issues, err)
	}
}

func TestScanNestedIgnoreRulesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"keep.mmd":          "graph TD\n",
		"nested/keep.mmd":   "graph TD\n",
		"nested/skip.mmd":   "graph TD\n",
		"nested/.gitignore": "skip.mmd\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A loop must not cause unbounded recursive scanning. Symlink creation can
	// be unavailable on Windows without developer mode; the ignore case remains
	// meaningful there.
	_ = os.Symlink(root, filepath.Join(root, "nested", "loop"))
	_ = os.Symlink(filepath.Join(root, "keep.mmd"), filepath.Join(root, "alias.mmd"))
	diagrams, issues, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil || len(issues) != 0 || len(diagrams) != 2 {
		t.Fatalf("nested ignore/symlink handling: %+v, %+v, %v", diagrams, issues, err)
	}
}

func TestScanCancellationAndBadTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Scan(ctx, t.TempDir(), ScanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan returned %v", err)
	}
	if _, _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "missing"), ScanOptions{}); err == nil {
		t.Fatal("missing target succeeded")
	}
	path := filepath.Join(t.TempDir(), "unsupported.txt")
	if err := os.WriteFile(path, []byte("graph TD"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Scan(context.Background(), path, ScanOptions{}); err == nil {
		t.Fatal("unsupported explicit input succeeded")
	}
	diagrams, issues, err := Scan(context.Background(), t.TempDir(), ScanOptions{})
	if err != nil || diagrams == nil || issues == nil || len(diagrams) != 0 || len(issues) != 0 {
		t.Fatalf("empty directory: %+v, %+v, %v", diagrams, issues, err)
	}
}
