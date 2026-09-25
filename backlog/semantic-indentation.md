# Mermaid-aware indentation

Status: research
Priority: P?
Effort: M

The user explicitly deferred semantic indentation. v1 uses Neovim autoindent,
two-space tabs and optional upstream Tree-sitter highlighting.

## Findings

- Official `mermaid.parse()` validates and identifies a diagram; it does not
  expose one stable public AST covering every diagram family.
- nvim-treesitter has upstream Mermaid indent queries. The third-party Mermaid
  grammar has incomplete coverage, so it cannot define canonical validity.
- The editor is already Neovim. A future change can enable upstream indent
  machinery without adding Mermaid-specific rules to Go or Lua.
- The official worker remains the authority for syntax and image rendering.

## Evaluation

Pin an upstream grammar/query combination and test subgraph/end, sequence,
class/state, frontmatter, and new unsupported diagram families. Missing nodes
or parse errors must fall back to normal indentation and never block saving or
rendering. Measure typing responsiveness with large diagrams. No owned grammar
or formatter is an acceptable fallback.

Sources:
- https://github.com/nvim-treesitter/nvim-treesitter
- https://github.com/monaqa/tree-sitter-mermaid
- https://mermaid.js.org/config/usage.html
