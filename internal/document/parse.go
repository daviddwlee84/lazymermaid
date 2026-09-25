package document

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Parse delegates Markdown grammar to goldmark. Fences are observed through a
// wrapper around goldmark's own parser, including empty and unterminated blocks.
// No Mermaid grammar, validation, or diagram-type detection happens here.
func Parse(path string, data []byte) (*Document, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%s: expected UTF-8 text without NUL bytes", path)
	}
	path = filepath.Clean(path)
	extension := strings.ToLower(filepath.Ext(path))
	if !supportedExtension(extension) {
		return nil, fmt.Errorf("%s: supported extensions are .md, .markdown, .mmd, and .mermaid", path)
	}
	source := bytes.Clone(data)
	doc := &Document{Path: path, Source: source, Hash: sourceHash(source), Newline: newlineStyle(source), Diagrams: []Diagram{}}
	if extension == ".mmd" || extension == ".mermaid" {
		endLine := endLineNumber(source, 0, len(source))
		if endLine == 0 {
			endLine = 1
		}
		doc.Diagrams = append(doc.Diagrams, Diagram{
			ID: path + "#1", Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Source: string(source), StartLine: 1, EndLine: endLine,
			BodyStartLine: 1, BodyEndLine: endLineNumber(source, 0, len(source)),
			EndByte: len(source), BodyEndByte: len(source),
			Closed: true, TopLevel: true, DocumentHash: doc.Hash, Standalone: true,
		})
		return doc, nil
	}
	tracker := &fenceTracker{BlockParser: parser.NewFencedCodeBlockParser(), source: source, spans: make(map[ast.Node]*fenceSpan)}
	// 700 is goldmark's documented default fenced-code priority. The wrapper
	// tries the identical upstream parser immediately before that default.
	markdown := goldmark.New(goldmark.WithParserOptions(parser.WithBlockParsers(util.Prioritized(tracker, 699))))
	root := markdown.Parser().Parse(text.NewReader(source))
	heading := ""
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := node.(*ast.Heading); ok {
			heading = headingText(h, source)
		}
		block, ok := node.(*ast.FencedCodeBlock)
		if !ok || !bytes.EqualFold(block.Language(source), []byte("mermaid")) {
			return ast.WalkContinue, nil
		}
		span, ok := tracker.spans[node]
		if !ok {
			return ast.WalkStop, fmt.Errorf("%s: code block has no source mapping", path)
		}
		end, bodyEnd := span.contentEnd, span.contentEnd
		if span.closed {
			end, bodyEnd = span.closeEnd, span.closeStart
		}
		startLine := lineNumber(source, span.start)
		title := heading
		if title == "" {
			title = fmt.Sprintf("%s:%d", filepath.Base(path), startLine)
		}
		topLevel := node.Parent() == root
		doc.Diagrams = append(doc.Diagrams, Diagram{
			ID: path + "#" + strconv.Itoa(len(doc.Diagrams)+1), Path: path, Title: title,
			Source:    string(block.Lines().Value(source)),
			StartLine: startLine, EndLine: endLineNumber(source, span.start, end),
			BodyStartLine: lineNumber(source, span.openEnd), BodyEndLine: endLineNumber(source, span.openEnd, bodyEnd),
			StartByte: span.start, EndByte: end, BodyStartByte: span.openEnd, BodyEndByte: bodyEnd,
			Closed: span.closed, TopLevel: topLevel,
			VirtualEditable: span.closed && topLevel && doc.Newline != "mixed",
			DocumentHash:    doc.Hash,
		})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func supportedExtension(extension string) bool {
	switch extension {
	case ".md", ".markdown", ".mmd", ".mermaid":
		return true
	default:
		return false
	}
}

type fenceSpan struct {
	start, openEnd, contentEnd int
	closeStart, closeEnd       int
	closed                     bool
}

type fenceTracker struct {
	parser.BlockParser
	source []byte
	spans  map[ast.Node]*fenceSpan
}

func (f *fenceTracker) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	_, segment := reader.PeekLine()
	node, state := f.BlockParser.Open(parent, reader, pc)
	if node != nil {
		start := physicalLineStart(f.source, segment.Start)
		end := physicalLineEnd(f.source, segment.Start)
		f.spans[node] = &fenceSpan{start: start, openEnd: end, contentEnd: end}
	}
	return node, state
}

func (f *fenceTracker) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	_, segment := reader.PeekLine()
	state := f.BlockParser.Continue(node, reader, pc)
	if span, ok := f.spans[node]; ok {
		if state&parser.Close != 0 {
			span.closed = true
			span.closeStart = physicalLineStart(f.source, segment.Start)
			span.closeEnd = physicalLineEnd(f.source, segment.Start)
		} else {
			span.contentEnd = physicalLineEnd(f.source, segment.Start)
		}
	}
	return state
}

func headingText(node ast.Node, source []byte) string {
	var value strings.Builder
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := child.(type) {
		case *ast.Text:
			value.Write(n.Segment.Value(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				value.WriteByte(' ')
			}
		case *ast.String:
			value.Write(n.Value)
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(value.String())
}

func newlineStyle(source []byte) string {
	lf, crlf := false, false
	for i, b := range source {
		if b == '\r' && (i+1 == len(source) || source[i+1] != '\n') {
			return "mixed"
		}
		if b == '\n' {
			if i > 0 && source[i-1] == '\r' {
				crlf = true
			} else {
				lf = true
			}
		}
	}
	if lf && crlf {
		return "mixed"
	}
	if crlf {
		return "\r\n"
	}
	return "\n"
}

func physicalLineStart(source []byte, offset int) int {
	if offset > len(source) {
		offset = len(source)
	}
	if offset <= 0 {
		return 0
	}
	return bytes.LastIndexByte(source[:offset], '\n') + 1
}

func physicalLineEnd(source []byte, offset int) int {
	if offset >= len(source) {
		return len(source)
	}
	if i := bytes.IndexByte(source[offset:], '\n'); i >= 0 {
		return offset + i + 1
	}
	return len(source)
}

func lineNumber(source []byte, offset int) int {
	return bytes.Count(source[:offset], []byte{'\n'}) + 1
}

func endLineNumber(source []byte, start, end int) int {
	if end <= start {
		return lineNumber(source, start) - 1
	}
	return lineNumber(source, end-1)
}
