package document

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// PrepareEdit returns a new parent document, without touching any file or editor.
// The selected diagram must come from this exact parent snapshot. Callers must
// still recheck the editor changedtick and disk identity before applying it.
// Mermaid syntax is deliberately not validated: invalid drafts may be saved.
func PrepareEdit(path, parent string, diagram Diagram, replacement string) (string, error) {
	if filepath.Clean(path) != filepath.Clean(diagram.Path) {
		return "", fmt.Errorf("%w: selected path differs", ErrStaleDocument)
	}
	doc, err := Parse(path, []byte(parent))
	if err != nil {
		return "", err
	}
	if diagram.DocumentHash == "" || doc.Hash != diagram.DocumentHash {
		return "", ErrStaleDocument
	}
	var current *Diagram
	for i := range doc.Diagrams {
		candidate := &doc.Diagrams[i]
		if candidate.ID == diagram.ID {
			current = candidate
			break
		}
	}
	if current == nil || !sameSelection(*current, diagram) {
		return "", ErrStaleDocument
	}
	if !current.VirtualEditable || current.Standalone {
		return "", ErrNotEditable
	}
	if !utf8.ValidString(replacement) || strings.IndexByte(replacement, 0) >= 0 {
		return "", fmt.Errorf("replacement must be UTF-8 text without NUL bytes")
	}
	body := strings.ReplaceAll(replacement, "\r\n", "\n")
	if strings.ContainsRune(body, '\r') {
		return "", fmt.Errorf("replacement must use LF or CRLF line endings")
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if body == strings.ReplaceAll(current.Source, "\r\n", "\n") {
		return parent, nil
	}
	open := parent[current.StartByte:current.BodyStartByte]
	close := parent[current.BodyEndByte:current.EndByte]
	opening, err := fenceEnvelope(open)
	if err != nil {
		return "", err
	}
	closing, err := fenceEnvelope(close)
	if err != nil || closing.char != opening.char || closing.length < opening.length {
		return "", fmt.Errorf("%w: closing fence has changed", ErrInvalidEdit)
	}
	// Counting all runs is intentionally conservative: it only chooses a safe
	// delimiter length and does not interpret either Markdown or Mermaid text.
	fenceLength := opening.length
	if run := longestRun(body, opening.char); run >= fenceLength {
		fenceLength = run + 1
	}
	open = opening.withLength(fenceLength)
	if closing.length < fenceLength {
		close = closing.withLength(fenceLength)
	}
	var encoded strings.Builder
	for body != "" {
		i := strings.IndexByte(body, '\n')
		encoded.WriteString(opening.prefix)
		encoded.WriteString(body[:i])
		encoded.WriteString(doc.Newline)
		body = body[i+1:]
	}
	updatedBlock := open + encoded.String() + close
	updated := parent[:current.StartByte] + updatedBlock + parent[current.EndByte:]
	verified, err := Parse(path, []byte(updated))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidEdit, err)
	}
	if len(verified.Diagrams) != len(doc.Diagrams) {
		return "", ErrInvalidEdit
	}
	wantedBody := strings.ReplaceAll(replacement, "\r\n", "\n")
	if wantedBody != "" && !strings.HasSuffix(wantedBody, "\n") {
		wantedBody += "\n"
	}
	delta := len(updated) - len(parent)
	for i, before := range doc.Diagrams {
		after := verified.Diagrams[i]
		if before.ID == current.ID {
			if !after.VirtualEditable || after.StartByte != before.StartByte || after.EndByte != before.EndByte+delta || strings.ReplaceAll(after.Source, "\r\n", "\n") != wantedBody {
				return "", ErrInvalidEdit
			}
			continue
		}
		expectedStart, expectedEnd := before.StartByte, before.EndByte
		if before.StartByte >= current.EndByte {
			expectedStart += delta
			expectedEnd += delta
		}
		if after.Source != before.Source || after.StartByte != expectedStart || after.EndByte != expectedEnd || after.Closed != before.Closed || after.TopLevel != before.TopLevel {
			return "", ErrInvalidEdit
		}
	}
	return updated, nil
}

func sameSelection(a, b Diagram) bool {
	return a.StartByte == b.StartByte && a.EndByte == b.EndByte &&
		a.BodyStartByte == b.BodyStartByte && a.BodyEndByte == b.BodyEndByte &&
		a.Source == b.Source && a.Closed == b.Closed && a.TopLevel == b.TopLevel &&
		a.Standalone == b.Standalone
}

type envelope struct {
	prefix, suffix string
	char           byte
	length         int
}

// fenceEnvelope inspects only a source line already accepted by goldmark. It
// preserves indentation, info metadata, closing whitespace, and newline bytes.
func fenceEnvelope(line string) (envelope, error) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i > 3 || i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return envelope{}, fmt.Errorf("%w: unsupported fence envelope", ErrInvalidEdit)
	}
	char := line[i]
	end := i
	for end < len(line) && line[end] == char {
		end++
	}
	if end-i < 3 {
		return envelope{}, ErrInvalidEdit
	}
	return envelope{prefix: line[:i], suffix: line[end:], char: char, length: end - i}, nil
}

func (e envelope) withLength(length int) string {
	return e.prefix + strings.Repeat(string(e.char), length) + e.suffix
}

func longestRun(value string, char byte) int {
	longest, run := 0, 0
	for i := 0; i < len(value); i++ {
		if value[i] == char {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return longest
}
