package document

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFencedBlocksAndSourceRanges(t *testing.T) {
	tests := []struct {
		name, input, body, rawBody string
		closed, topLevel           bool
	}{
		{"plain", "```mermaid\ngraph TD\nA-->B\n```\n", "graph TD\nA-->B\n", "graph TD\nA-->B\n", true, true},
		{"unicode metadata", "~~~MeRmAiD title=系統\nflowchart LR\n甲-->乙\n~~~~~  \n", "flowchart LR\n甲-->乙\n", "flowchart LR\n甲-->乙\n", true, true},
		{"indent", "  ```mermaid\n  graph TD\n    A-->B\n  ```\n", "graph TD\n  A-->B\n", "  graph TD\n    A-->B\n", true, true},
		{"list", "- ```mermaid\n  graph TD\n  A-->B\n  ```\n", "graph TD\nA-->B\n", "  graph TD\n  A-->B\n", true, false},
		{"blockquote", "> ```mermaid\n> graph TD\n> A-->B\n> ```\n", "graph TD\nA-->B\n", "> graph TD\n> A-->B\n", true, false},
		{"empty", "```mermaid\n```\n", "", "", true, true},
		{"empty quoted", "> ```mermaid\n> ```\n", "", "", true, false},
		{"empty unclosed", "```mermaid\n", "", "", false, true},
		{"unterminated", "```mermaid\ngraph TD\nA-->B", "graph TD\nA-->B\n", "graph TD\nA-->B", false, true},
		{"closing at EOF", "```mermaid\ngraph TD\n```", "graph TD\n", "graph TD\n", true, true},
		{"CRLF", "```mermaid\r\ngraph TD\r\nA-->B\r\n```\r\n", "graph TD\r\nA-->B\r\n", "graph TD\r\nA-->B\r\n", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Parse("guide.md", []byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Diagrams) != 1 {
				t.Fatalf("got %d diagrams: %+v", len(doc.Diagrams), doc.Diagrams)
			}
			d := doc.Diagrams[0]
			if d.Source != tt.body {
				t.Errorf("body = %q; want %q", d.Source, tt.body)
			}
			if got := string(doc.Source[d.BodyStartByte:d.BodyEndByte]); got != tt.rawBody {
				t.Errorf("body range = %q; want %q", got, tt.rawBody)
			}
			if got := string(doc.Source[d.StartByte:d.EndByte]); got != tt.input {
				t.Errorf("full block range = %q; want %q", got, tt.input)
			}
			if d.Closed != tt.closed || d.TopLevel != tt.topLevel || d.VirtualEditable != (tt.closed && tt.topLevel) {
				t.Errorf("incorrect edit capabilities: %+v", d)
			}
			if d.DocumentHash != doc.Hash || len(doc.Hash) != 64 {
				t.Error("missing snapshot identity")
			}
			if tt.body == "" && d.BodyEndLine != d.BodyStartLine-1 {
				t.Errorf("empty body line range: %d..%d", d.BodyStartLine, d.BodyEndLine)
			}
		})
	}
}

func TestParseContainersAndHeadings(t *testing.T) {
	input := "# 系統 **設計**\n\n```mermaid\ngraph TD\nA-->B\n```\n\n## Quoted\n\n> ```mermaid\n> graph LR\n\nparagraph\n\n```mermaid\nsequenceDiagram\n```\n"
	doc, err := Parse("中文.md", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagrams) != 3 {
		t.Fatalf("got %+v", doc.Diagrams)
	}
	first, nested, last := doc.Diagrams[0], doc.Diagrams[1], doc.Diagrams[2]
	if first.Title != "系統 設計" || first.StartLine != 3 || first.EndLine != 6 || first.BodyStartLine != 4 || first.BodyEndLine != 5 {
		t.Fatalf("wrong heading or line mapping: %+v", first)
	}
	if first.StartByte != strings.Index(input, "```mermaid") {
		t.Errorf("Unicode text changed byte offset: %d", first.StartByte)
	}
	if nested.Closed || nested.EndLine != 11 || nested.Source != "graph LR\n" {
		t.Errorf("unterminated container consumed following prose: %+v", nested)
	}
	if last.StartLine != 15 || last.Title != "Quoted" {
		t.Errorf("later block lost: %+v", last)
	}
}

func TestParseOnlyActualMermaidFences(t *testing.T) {
	input := "inline `mermaid`\n\n````text\n```mermaid\ngraph TD\n```\n````\n\n    ```mermaid\n    graph TD\n    ```\n\n<pre>\n```mermaid\ngraph TD\n```\n</pre>\n\n```mermaidish\ngraph TD\n```\n"
	doc, err := Parse("guide.markdown", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagrams) != 0 {
		t.Fatalf("detected non-Mermaid fences: %+v", doc.Diagrams)
	}
}

func TestParseStandaloneAndInputValidation(t *testing.T) {
	data := []byte("flowchart TD\n甲-->乙")
	doc, err := Parse("chart.MMD", data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	d := doc.Diagrams[0]
	if !d.Standalone || d.VirtualEditable || d.Source != "flowchart TD\n甲-->乙" || string(doc.Source) != d.Source || d.Title != "chart" {
		t.Fatalf("incorrect standalone source: %+v", d)
	}
	for _, invalid := range []struct {
		path string
		data []byte
	}{{"chart.txt", []byte("graph TD")}, {"chart.md", []byte{0xff}}, {"chart.mmd", []byte{'a', 0, 'b'}}} {
		if _, err := Parse(invalid.path, invalid.data); err == nil {
			t.Errorf("accepted invalid input %q, %v", invalid.path, invalid.data)
		}
	}
	empty, err := Parse("empty.mermaid", nil)
	if err != nil || len(empty.Diagrams) != 1 || empty.Diagrams[0].EndLine != 1 {
		t.Fatalf("empty standalone file must remain selectable: %+v, %v", empty, err)
	}
}

func TestMixedNewlinesArePreviewOnly(t *testing.T) {
	doc, err := Parse("mixed.md", []byte("```mermaid\r\ngraph TD\n```\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Newline != "mixed" || doc.Diagrams[0].VirtualEditable {
		t.Fatalf("mixed newline document marked editable: %+v", doc)
	}
}

func TestSelectDoesNotGuess(t *testing.T) {
	doc, err := Parse("two.md", []byte("intro\n\n```mermaid\na\n```\n\n```mermaid\nb\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(doc.Diagrams, 0); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ambiguous selection, got %v", err)
	}
	if _, err := Select(doc.Diagrams, 2); !errors.Is(err, ErrNoDiagram) {
		t.Fatalf("selected a nearby block instead of rejecting: %v", err)
	}
	for _, line := range []int{3, 4, 5} {
		selected, err := Select(doc.Diagrams, line)
		if err != nil || selected.ID != doc.Diagrams[0].ID {
			t.Fatalf("line %d: %+v, %v", line, selected, err)
		}
	}
	if _, err := Select(nil, 0); !errors.Is(err, ErrNoDiagram) {
		t.Fatal(err)
	}
}
