# lazymermaid

A repository-aware Mermaid workbench for the terminal. Discover diagrams in
Markdown, edit their real source with an embedded Neovim, and preview unsaved
changes as official Mermaid images or termaid Unicode/ASCII art.

lazymermaid integrates upstream tools. It does not implement Mermaid syntax,
layout, an ASCII renderer, or an editor engine.

## Install / 安裝

```sh
brew install daviddwlee84/tap/lazymermaid
lazymermaid --version
lazymermaid upgrade --check
```

**v0.1.0** adds macOS/Linux amd64/arm64 binary releases and the personal Homebrew
formula. Go is optional for binary installs; runtime backends remain separate.
See [installation, completion and owner-aware upgrades](docs/distribution.md).
[MIT license](LICENSE).

## Build and start

Requires Go 1.26.5 or newer to build. macOS and Linux are the initial targets.

```sh
go build -o bin/lazymermaid ./cmd/lazymermaid
./bin/lazymermaid doctor
./bin/lazymermaid examples
# Or scan your own repository:
./bin/lazymermaid .
```

The explorer and handbook work without optional tools. Install the capabilities
you want:

| Capability | Optional dependency |
| --- | --- |
| In-pane editing, Vim, undo/redo | Neovim |
| Unicode / ASCII preview and export | [termaid](https://github.com/fasouto/termaid) (`uv tool install termaid`) |
| Official validation, SVG / PNG | Node ≥22.12 + the pinned Mermaid/Puppeteer runtime below |
| Mermaid syntax highlighting | Upstream Tree-sitter parser and queries; see [development](docs/development.md) |
| In-terminal images | Kitty graphics protocol, primarily Kitty / Ghostty |

```sh
npm ci --prefix runtime/mermaid
npm exec --prefix runtime/mermaid -- puppeteer browsers install chrome
```

A checkout automatically uses its `runtime/mermaid` directory. An installed
binary can prepare the same pinned manifests explicitly:

```sh
lazymermaid runtime prepare
# Then run the npm commands printed by that command.
```

`doctor` reports paths, versions, missing capabilities and installation hints.
Startup and doctor never install packages or enable tmux passthrough. Rendering
uses local dependencies and a private loopback server; it does not upload diagrams.

## Workflow

The list shows Mermaid blocks with titles and original source lines. The source
pane is a real isolated Neovim. The preview follows its unsaved buffer, not just
filesystem saves. Wide terminals show three columns; narrower terminals stack
source/preview or show the focused pane.

| Key, outside Neovim | Action |
| --- | --- |
| arrows / `hjkl`, `gg` / `G` | Navigate; pan when preview is focused |
| `Enter`, `e` | Open original source at the block |
| `Ctrl-G` | Cycle panes, including from Neovim |
| `Tab` / `Shift-Tab` | Cycle panes outside Neovim |
| `/` | Filter diagrams; Enter accepts, Esc clears |
| `v` | Edit only this diagram in a virtual buffer |
| `n` | New Mermaid scratch buffer; `:w path.mmd` saves it |
| `t`, `T` | Cycle image / Unicode / ASCII, or Mermaid theme |
| `+` / `-`, `f` | Image zoom / fit |
| `r`, `R` | Render / rescan |
| `d` | Full renderer / syntax diagnostic details |
| `x`, `y` | Export / copy text preview via terminal clipboard |
| `F1`, `?` | Searchable syntax handbook / key help |
| `q` | Quit; unsaved buffers require a deliberate choice |

While Neovim is focused, its normal input, Tab, Escape, registers, macros,
undo/redo and `:w` remain Neovim operations. Use Ctrl-G to leave the editor.
Handbook pages support `n` to open their first Mermaid example as a scratch.

Virtual buffers are offered for **closed, top-level Markdown fences** with a
saved parent. `:w` updates just that block and lets Neovim save the parent file.
External changes cause a conflict instead of a silent overwrite. Drafts survive
failed writes. Nested list/blockquote diagrams remain editable in the original
file. Invalid Mermaid can still be saved as a draft.

## CLI

```sh
lazymermaid scan docs --json
lazymermaid render diagram.mmd --format svg -o diagram.svg
lazymermaid render README.md --line 42 --format png -o diagram.png
cat diagram.mmd | lazymermaid render - --format ascii
lazymermaid check docs --json
lazymermaid docs sequence
lazymermaid docs sequenceDiagram
lazymermaid config show --json
lazymermaid completion zsh
```

`--line` selects a block containing that 1-based source line. Multiple blocks
require a selector. Binary PNG output to an interactive terminal requires `-o`.
Non-TTY and JSON commands never prompt; stdout contains results and stderr errors.
Exit codes: 0 success, 1 operation failure, 2 usage/configuration error, 130 cancel.

Standalone files: `.mmd`, `.mermaid`. Markdown: `.md`, `.markdown`. Discovery uses
goldmark's fenced-code AST and source segments, including nested blocks; it
honors `.gitignore` / `.ignore`, skips hidden files and symlinks by default, and
never guesses a Mermaid diagram type from a private grammar.

## Configuration

Configuration is optional. The default is
`$XDG_CONFIG_HOME/lazymermaid/config.toml`, or
`~/.config/lazymermaid/config.toml`, including on macOS. `--config` selects an
explicit file; a missing explicit file is an error. Project configuration is not
automatically loaded.

```toml
[editor]
command = "nvim"
runtime_paths = []

[render]
node = "node"
termaid = "termaid"
# runtime_dir = "/absolute/path/to/runtime/mermaid"
debounce_ms = 300
timeout_seconds = 30
# Optional upstream Mermaid settings, passed as JSON without a private schema:
# mermaid_config = '{"flowchart":{"curve":"linear"}}'

[preview]
mode = "auto" # auto | image | unicode | ascii
theme = "dark"

[scan]
hidden = false
no_ignore = false
```

Explicit flags override documented environment variables, then TOML, then
defaults. Environment overrides: `LAZYMERMAID_NVIM`, `LAZYMERMAID_NODE`,
`LAZYMERMAID_RUNTIME_DIR`, `LAZYMERMAID_TERMAID`, `LAZYMERMAID_PREVIEW`, and
`LAZYMERMAID_THEME`. Commands are executable paths, not shell fragments.
`NO_COLOR` and `--color auto|always|never` control host color.

## Compatibility and status

The official runtime and bundled handbook use Mermaid **12.0.0**. The renderer
reports its effective engine version; compatibility follows that version, not
the continuously deployed mermaid.live site. termaid has its own support range:
text-rendering failure is not an authoritative Mermaid syntax error.

Upstream Mermaid errors do not consistently carry original-source locations.
lazymermaid keeps the original message and marks the diagram block when exact
mapping is unavailable, instead of inventing a line number.

Image mode initially uses Kitty protocol. tmux needs graphics passthrough; SSH
uses direct image transfer. Environment detection is a candidate indication,
not proof that every terminal/multiplexer combination works. Unicode/ASCII and
SVG/PNG export remain available independently. Sixel, iTerm2-native graphics,
full MDX and semantic indentation are future work.

Use `lazymermaid upgrade --check` / `--yes` for a verified Homebrew installation.
Checkout users can still rebuild with `go install ./cmd/lazymermaid`; standalone
archives use their external installer. See [distribution](docs/distribution.md).

## Development

See [development and verification](docs/development.md). Long-term work is
indexed in [TODO.md](TODO.md), with research in [backlog/](backlog/) and verified
troubleshooting notes in [pitfalls/](pitfalls/).

The offline handbook is copied from the matching official Mermaid tag with its
[upstream MIT license](internal/handbook/LICENSE.upstream) and source metadata.
