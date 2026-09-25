// Package document discovers Mermaid sources without interpreting Mermaid syntax.
package document

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Diagram identifies one standalone source or fenced Markdown code block.
// Byte ranges are half-open. Line numbers are one-based and inclusive; an
// empty body has BodyEndLine == BodyStartLine-1. Ranges refer to Document.Source,
// while Source is the logical code body with Markdown container prefixes removed.
// ID is document-local: pair it with DocumentHash across refreshes, since inserting
// a preceding diagram changes the ordinals of later diagrams.
type Diagram struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	Title           string `json:"title"`
	Source          string `json:"source"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	BodyStartLine   int    `json:"body_start_line"`
	BodyEndLine     int    `json:"body_end_line"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	BodyStartByte   int    `json:"body_start_byte"`
	BodyEndByte     int    `json:"body_end_byte"`
	Closed          bool   `json:"closed"`
	TopLevel        bool   `json:"top_level"`
	VirtualEditable bool   `json:"virtual_editable"`
	DocumentHash    string `json:"document_hash"`
	Standalone      bool   `json:"standalone"`
}

type Document struct {
	Path     string    `json:"path"`
	Source   []byte    `json:"-"`
	Diagrams []Diagram `json:"diagrams"`
	Hash     string    `json:"hash"`
	// Newline is "\n", "\r\n", or "mixed". Files without newlines use "\n".
	Newline string `json:"newline"`
}

type ScanOptions struct {
	Hidden   bool
	NoIgnore bool
}

type ScanIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

var (
	ErrNoDiagram     = errors.New("no Mermaid diagram matches the selection")
	ErrAmbiguous     = errors.New("multiple Mermaid diagrams; select one by line")
	ErrStaleDocument = errors.New("document changed since the diagram was selected")
	ErrNotEditable   = errors.New("Mermaid-only editing requires a closed top-level Markdown fence")
	ErrInvalidEdit   = errors.New("edit would change Markdown outside the selected diagram")
)

func sourceHash(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}

// Select returns the diagram containing line. With line zero, exactly one
// diagram must be present. A selection outside a block never chooses a nearby
// block implicitly.
func Select(diagrams []Diagram, line int) (Diagram, error) {
	if line < 0 {
		return Diagram{}, fmt.Errorf("line must be positive: %d", line)
	}
	if line == 0 {
		switch len(diagrams) {
		case 0:
			return Diagram{}, ErrNoDiagram
		case 1:
			return diagrams[0], nil
		default:
			return Diagram{}, ErrAmbiguous
		}
	}
	var selected *Diagram
	for i := range diagrams {
		d := &diagrams[i]
		if line >= d.StartLine && line <= d.EndLine {
			if selected != nil {
				return Diagram{}, ErrAmbiguous
			}
			selected = d
		}
	}
	if selected == nil {
		return Diagram{}, fmt.Errorf("%w at line %d", ErrNoDiagram, line)
	}
	return *selected, nil
}
