package document

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPrepareEditPreservesMarkdown(t *testing.T) {
	tests := []struct {
		name, parent, replacement, want string
	}{
		{"body only", "# 中文\n\n```mermaid title=example\nold\n```  \n\nafter\n", "new\n甲-->乙", "# 中文\n\n```mermaid title=example\nnew\n甲-->乙\n```  \n\nafter\n"},
		{"indent", "before\n\n  ```mermaid\n  old\n ```\n\nafter", "graph TD\n  A-->B\n", "before\n\n  ```mermaid\n  graph TD\n    A-->B\n ```\n\nafter"},
		{"empty to body", "```mermaid\n```", "a", "```mermaid\na\n```"},
		{"body to empty", "```mermaid\na\n```\n", "", "```mermaid\n```\n"},
		{"CRLF", "before\r\n\r\n```mermaid x=y\r\na\r\n```\r\nafter", "甲\n乙\n", "before\r\n\r\n```mermaid x=y\r\n甲\r\n乙\r\n```\r\nafter"},
		{"fence collision", "```mermaid x=y\na\n```  \n", "a\n```\nb\n", "````mermaid x=y\na\n```\nb\n````  \n"},
		{"longer original closer", "~~~mermaid\na\n~~~~~~~\n", "a\n~~~~\n", "~~~~~mermaid\na\n~~~~\n~~~~~~~\n"},
		{"blank body lines", "```mermaid\na\n```\n", "\n\na\n\n", "```mermaid\n\n\na\n\n```\n"},
		{"invalid Mermaid still saves", "```mermaid\na\n```\n", "this is not Mermaid", "```mermaid\nthis is not Mermaid\n```\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Parse("guide.md", []byte(tt.parent))
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareEdit(doc.Path, tt.parent, doc.Diagrams[0], tt.replacement)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestPrepareEditKeepsOtherBlocksAndNoopBytes(t *testing.T) {
	parent := "# One\n\n```mermaid\na\n```\n\n# Two\n\n   ~~~mermaid  title=two\n b\n~~~\n\n> ```mermaid\n> c\n> ```\n"
	doc, err := Parse("guide.md", []byte(parent))
	if err != nil {
		t.Fatal(err)
	}
	d := doc.Diagrams[1]
	got, err := PrepareEdit(doc.Path, parent, d, d.Source)
	if err != nil || got != parent {
		t.Fatalf("no-op changed original bytes: %q, %v", got, err)
	}
	got, err = PrepareEdit(doc.Path, parent, d, "new\nsource\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, parent[:d.StartByte]) || !strings.HasSuffix(got, parent[d.EndByte:]) {
		t.Fatalf("changed other Markdown: %q", got)
	}
	updated, _ := Parse(doc.Path, []byte(got))
	if updated.Diagrams[0].Source != doc.Diagrams[0].Source || updated.Diagrams[2].Source != doc.Diagrams[2].Source {
		t.Fatal("changed another diagram")
	}
}

func TestPrepareEditRejectsStaleOrUnsafeSelection(t *testing.T) {
	parent := "```mermaid\na\n```\n"
	doc, _ := Parse("guide.md", []byte(parent))
	d := doc.Diagrams[0]
	for _, tt := range []struct {
		name, path, source string
		d                  Diagram
	}{
		{"changed after block", doc.Path, parent + "extra", d},
		{"changed before block", doc.Path, "extra\n" + parent, d},
		{"different path", "another.md", parent, d},
		{"forged boundary", doc.Path, parent, func() Diagram { x := d; x.BodyStartByte++; return x }()},
		{"forged identity", doc.Path, parent, func() Diagram { x := d; x.ID = "guide.md#2"; return x }()},
		{"missing snapshot", doc.Path, parent, func() Diagram { x := d; x.DocumentHash = ""; return x }()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := PrepareEdit(tt.path, tt.source, tt.d, "new"); !errors.Is(err, ErrStaleDocument) || got != "" {
				t.Fatalf("expected stale rejection, got %q, %v", got, err)
			}
		})
	}
	for _, source := range []string{"> ```mermaid\n> a\n> ```\n", "```mermaid\na", "```mermaid\r\na\n```\r\n"} {
		unsafe, err := Parse("guide.md", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareEdit(unsafe.Path, source, unsafe.Diagrams[0], "new"); !errors.Is(err, ErrNotEditable) {
			t.Errorf("unsafe source %q accepted: %v", source, err)
		}
	}
	for _, replacement := range []string{"a\x00b", "a\rb", string([]byte{0xff})} {
		if _, err := PrepareEdit(doc.Path, parent, d, replacement); err == nil {
			t.Errorf("invalid text %q accepted", replacement)
		}
	}
}

func FuzzPrepareEditKeepsTheEnvelope(f *testing.F) {
	for _, source := range []string{"", "a\n", "```\n~~~~\n", "甲-->乙", "\t\tline\n\n", "\"quote\"\\backslash\r\n"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, replacement string) {
		if len(replacement) > 8192 || !utf8.ValidString(replacement) || strings.ContainsRune(replacement, 0) {
			t.Skip()
		}
		normalized := strings.ReplaceAll(replacement, "\r\n", "\n")
		if strings.ContainsRune(normalized, '\r') {
			t.Skip()
		}
		parent := "# 前綴\n\n  ```mermaid title=original\n  old\n  ```  \n\nAfter **unchanged**\n"
		doc, err := Parse("guide.md", []byte(parent))
		if err != nil {
			t.Fatal(err)
		}
		d := doc.Diagrams[0]
		updated, err := PrepareEdit(doc.Path, parent, d, replacement)
		if err != nil {
			t.Fatalf("valid literal body rejected: %q: %v", replacement, err)
		}
		if !strings.HasPrefix(updated, parent[:d.StartByte]) || !strings.HasSuffix(updated, parent[d.EndByte:]) {
			t.Fatal("unrelated Markdown was modified")
		}
		parsed, err := Parse(doc.Path, []byte(updated))
		if err != nil || len(parsed.Diagrams) != 1 {
			t.Fatalf("edited diagram lost its fence: %v", err)
		}
		if normalized != "" && !strings.HasSuffix(normalized, "\n") {
			normalized += "\n"
		}
		if parsed.Diagrams[0].Source != normalized {
			t.Fatalf("roundtrip changed source: got %q, want %q", parsed.Diagrams[0].Source, normalized)
		}
	})
}
