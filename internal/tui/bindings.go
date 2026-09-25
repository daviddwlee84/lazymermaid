package tui

import (
	"fmt"
	"strings"
)

// Host bindings are shared by dispatch, contextual hints and help. Neovim owns
// its own bindings; no host printable binding runs while the editor has focus.
type action struct {
	id                 string
	keys               []string
	label, description string
}

var actions = []action{
	{"quit", []string{"q", "ctrl+c"}, "quit", "Quit; unsaved buffers are protected"},
	{"help", []string{"?"}, "help", "Contextual keyboard help"},
	{"docs", []string{"f1"}, "syntax", "Search official syntax; n uses a page example"},
	{"edit", []string{"e"}, "edit", "Open source at this diagram (Enter also opens)"},
	{"virtual", []string{"v"}, "Mermaid-only", "Project a saved, closed top-level block"},
	{"new", []string{"n"}, "new", "New Mermaid buffer; :w path.mmd saves"},
	{"filter", []string{"/"}, "filter", "Filter diagrams; Enter accepts, Esc clears"},
	{"mode", []string{"t"}, "mode", "Cycle image / Unicode / ASCII"},
	{"theme", []string{"T"}, "theme", "Cycle official Mermaid theme"},
	{"render", []string{"r"}, "render", "Render the latest source"},
	{"rescan", []string{"R"}, "rescan", "Refresh repository discovery"},
	{"diagnostics", []string{"d"}, "errors", "Full syntax / renderer diagnostics"},
	{"export", []string{"x"}, "export", "Export to a new SVG / PNG / text file"},
	{"copy", []string{"y"}, "copy", "Copy text preview via terminal clipboard"},
	{"zoom-in", []string{"+", "="}, "zoom in", "Zoom image in"},
	{"zoom-out", []string{"-"}, "zoom out", "Zoom image out"},
	{"fit", []string{"f"}, "fit", "Fit image / reset text scroll"},
}

func canonicalHostKey(key string) string {
	for _, a := range actions {
		for _, k := range a.keys {
			if key == k {
				return a.keys[0]
			}
		}
	}
	return key
}
func hints(ids ...string) string {
	var parts []string
	for _, id := range ids {
		for _, a := range actions {
			if a.id == id {
				key := a.keys[0]
				if key == "f1" {
					key = "F1"
				}
				parts = append(parts, key+" "+a.label)
				break
			}
		}
	}
	return strings.Join(parts, " · ")
}
func helpText() string {
	s := "Navigate    arrows / hjkl; gg / G for long lists\nFocus       Tab / Shift-Tab outside editor; Ctrl-G everywhere\n\n"
	for _, a := range actions {
		s += fmt.Sprintf("%-12s%s\n", strings.Join(a.keys, " / "), a.description)
	}
	return s + "\nTyping belongs to the focused input or Neovim.\nInside Neovim use :w to save and Ctrl-G to leave.\nMissing optional tools: lazymermaid doctor.\nEsc closes help."
}
