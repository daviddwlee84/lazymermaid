# Project agent guidance

lazymermaid is a Go CLI/TUI for discovering Mermaid diagrams in repositories,
editing with an embedded Neovim, and previewing through existing renderers.

## Constraints

- Do not implement Mermaid syntax, layout, ASCII rendering, or a terminal/editor
  engine. Use goldmark for Markdown, Neovim for editing, official Mermaid for
  validation/images, and termaid for text.
- Treat third-party text-renderer failures separately from official syntax
  errors. Do not invent exact source locations when upstream coordinates are
  unreliable.
- Neovim owns source-file writes and undo. Virtual block saves use validated
  source ranges plus parent/disk conflict checks; preserve drafts on failure.
- Keep optional dependencies optional. Startup and doctor never install tools,
  modify user Neovim/tmux configuration, or send diagrams to a remote service.
- Follow `.agents/skills/go-cli-tui/SKILL.md` for host UI/CLI work.

## Verified development commands

```sh
go build -o bin/lazymermaid ./cmd/lazymermaid
go test ./...
go test -race ./internal/editor ./internal/document ./internal/render ./internal/handbook
go vet ./...
LAZYMERMAID_TEST_RUNTIME="$PWD/runtime/mermaid" go test ./internal/render -run TestOfficialRuntime -v
```

The optional Node runtime is installed with `npm ci --prefix runtime/mermaid`
and `npm exec --prefix runtime/mermaid -- puppeteer browsers install chrome`.
The worker and handbook are embedded in the binary. Runtime dependencies remain
external. See `docs/development.md` for PTY testing and local termaid setup.

## Non-obvious architecture

The outer Bubble Tea program alone reads the real terminal. Neovim runs in a
bubbleterm PTY and has an independent local RPC socket for unsaved snapshots,
source navigation, diagnostics, and virtual buffers. Renderer requests carry
revision identity; stale success and stale failure must both be ignored.

Diagram ordinal IDs are reliable only within their DocumentHash. Across source
changes, use cursor/source identity, not the previous list row or ordinal alone.

The graphics output wrapper must retain the underlying terminal's `term.File`
interface (Read/Write/Close/Fd); hiding it makes Bubble Tea measure a 0×0 screen.
Close cleans owned graphics resources, not the user's stdout descriptor.

## Project memory

`TODO.md` is the priority/effort-tagged index of future work. Research belongs in
`backlog/`; verified past traps belong in symptom-named `pitfalls/` documents.
Use the project-knowledge-harness skill's init/add-todo/promote/validation tools
when available. Do not create competing roadmap/ideas indexes or fill files with
unverified commands. Semantic indentation is deliberately deferred.
