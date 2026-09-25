package app

import (
	"context"
	"encoding/json"
	"fmt"
	"lazymermaid/internal/config"
	"lazymermaid/internal/graphics"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Capability struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func Doctor(ctx context.Context, c config.Config) []Capability {
	checks := []struct{ name, command, hint string }{{"Neovim", c.Editor.Command, "Install Neovim to enable in-pane editing; macOS: brew install neovim"}, {"Node", c.Render.Node, "Install Node.js >=22.12 to use the official Mermaid runtime"}, {"termaid", c.Render.Termaid, "Install text rendering: uv tool install termaid (or pipx install termaid)"}}
	result := make([]Capability, 0, 6)
	for _, ch := range checks {
		p, e := exec.LookPath(ch.command)
		r := Capability{Name: ch.name, Status: "available", Detail: p}
		if e != nil {
			r.Status = "optional missing"
			r.Detail = e.Error()
			r.Hint = ch.hint
		} else {
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			b, e := exec.CommandContext(bounded, p, "--version").CombinedOutput()
			cancel()
			if e == nil {
				r.Detail += " · " + strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
			}
		}
		result = append(result, r)
	}
	r := Capability{Name: "Mermaid runtime", Status: "optional missing", Detail: c.Render.RuntimeDir, Hint: fmt.Sprintf("In the checkout run npm ci --prefix runtime/mermaid; set LAZYMERMAID_RUNTIME_DIR to that absolute directory (%s)", filepath.Join(c.Render.RuntimeDir, "package.json"))}
	s := NewRenderer(c)
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	info, e := s.Info(bounded)
	cancel()
	_ = s.Close()
	if e == nil {
		r.Status = "available"
		r.Detail = fmt.Sprintf("Mermaid %s · %s", info.MermaidVersion, info.BrowserVersion)
		r.Hint = ""
	} else {
		r.Detail = e.Error()
	}
	result = append(result, r)
	g := Capability{Name: "Kitty graphics", Status: "unknown", Detail: "environment check only; Unicode/ASCII remain available"}
	if graphics.Available() {
		g.Status = "candidate"
		g.Detail = "compatible environment; verify image preview in this terminal"
	}
	if os.Getenv("TMUX") != "" {
		g.Hint = "For images tmux needs allow-passthrough on; doctor does not change it"
	}
	result = append(result, g)
	parserCheck := Capability{Name: "Tree-sitter", Status: "optional missing", Detail: "Mermaid parser/query not detected", Hint: "See docs/development.md for isolated editor.runtime_paths; editing works without parsers"}
	paths, _ := json.Marshal(c.Editor.RuntimePaths)
	bounded, cancel = context.WithTimeout(ctx, 3*time.Second)
	probe := exec.CommandContext(bounded, c.Editor.Command, "--headless", "-u", "NONE", "-i", "NONE", "--noplugin", "-c", `lua vim.opt.runtimepath={vim.env.VIMRUNTIME}; for _,p in ipairs(vim.json.decode(vim.env.LAZYMERMAID_PROBE_PATHS)) do vim.opt.runtimepath:append(p) end; local ok=pcall(vim.treesitter.language.add,'mermaid'); local qok,q=pcall(vim.treesitter.query.get,'mermaid','highlights'); print(ok and qok and q~=nil and 'MERMAID_PARSER_OK' or 'MERMAID_PARSER_MISSING')`, "-c", "qa!")
	probe.Env = append(os.Environ(), "LAZYMERMAID_PROBE_PATHS="+string(paths))
	b, pe := probe.CombinedOutput()
	cancel()
	if pe == nil && strings.Contains(string(b), "MERMAID_PARSER_OK") {
		parserCheck.Status = "available"
		parserCheck.Detail = "Mermaid parser and highlight query loaded in the configured isolated runtime"
		parserCheck.Hint = ""
	}
	result = append(result, parserCheck)
	return result
}
