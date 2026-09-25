# Development and verification

## Commands

```sh
go build -o bin/lazymermaid ./cmd/lazymermaid
go vet ./...
go test ./...
go test -race ./...
python3 scripts/pty_smoke.py --binary ./bin/lazymermaid
```

Editor integration tests use a real Neovim when one is installed. The PTY smoke
uses disposable files/configuration and a fake text renderer, while running the
real application, terminal lifecycle and Neovim. It does not claim to visually
validate Kitty pixels on a physical terminal.

After installing the optional official runtime:

```sh
LAZYMERMAID_TEST_RUNTIME="$PWD/runtime/mermaid" go test ./internal/render -run TestOfficialRuntime -v
./bin/lazymermaid render examples/architecture.md --line 5 --format svg -o /tmp/lazymermaid-example.svg
./bin/lazymermaid render examples/architecture.md --line 5 --format png -o /tmp/lazymermaid-example.png
```

For a checkout-local text renderer without changing your system tools:

```sh
uv venv .venv
uv pip install --python .venv/bin/python termaid
LAZYMERMAID_TERMAID="$PWD/.venv/bin/termaid" ./bin/lazymermaid examples
```

## Architecture

- `internal/document`: goldmark-based discovery, position recording, pure guarded
  projection edits. Mermaid source is opaque; headings supply display titles.
- `internal/editor`: real Neovim PTY through bubbleterm plus a separate local RPC
  socket. Lua is the editor adapter; Neovim owns writes and undo.
- `internal/render`: persistent local official Mermaid worker and termaid process
  adapter. Renderer errors, unavailability, and syntax errors are distinct.
- `internal/tui` and `internal/app`: host UI and Cobra adapters sharing the same
  document and render operations.
- `internal/graphics`: Charm Kitty protocol encoders and a serialized terminal
  output presenter; no Mermaid layout or image-to-text implementation.
- `internal/handbook`: source/version-tagged upstream docs and offline search.

The outer Bubble Tea program alone owns the user's terminal. Neovim reads its
private PTY; RPC events carry unsaved snapshots. Keep process callbacks outside
UI state and return messages. Renderer results must match the active revision.

## Isolated Tree-sitter support

The embedded editor does not load personal Neovim configuration or plugins.
Basic Vim editing and `autoindent` work without any Tree-sitter installation.
The supported extension is `editor.runtime_paths`: absolute upstream runtime
directories containing Neovim-compatible `parser/mermaid.*` and
`queries/mermaid/highlights.scm`; Markdown injections additionally need the
Markdown parsers/queries. Paths are appended only inside the embedded editor.

Use upstream [nvim-treesitter](https://github.com/nvim-treesitter/nvim-treesitter)
installation instructions for the pinned Neovim version to produce those files,
then point `runtime_paths` at that installation's runtime directories. There is
no background plugin installer and no custom Mermaid grammar. The optional
loader catches missing/unsupported parsers so editing remains available.

Tree-sitter errors never determine Mermaid validity. Semantic indentation is
explicitly deferred; see the research linked from TODO.md.

## Dependency updates

Keep Charm modules on compatible v2 majors. `bubbleterm v0.3.5` uses upstream
`x/vt`; this project explicitly selects the later September 1, 2026 x/vt fix for
combining characters. Test Chinese, combining marks, emoji, paste and resize
before changing the editor dependencies.

Upgrade the official renderer's exact versions and lockfile together with the
handbook snapshot. `python3 internal/handbook/sync.py` fetches the documented
Mermaid tag and its MIT license. Run fake-worker tests and the real browser
integration test; a successful text render does not verify official syntax.

For graphical acceptance, run in Kitty/Ghostty directly and under tmux: switch
images, resize rapidly, zoom/pan, open overlays, switch tmux panes, quit and check
for stale images or cursor changes. Use Unicode mode if a graphics path is not
supported. The protocol tests do not replace this physical-terminal check.
