# Changelog

## Unreleased

## v0.1.1 - 2026-09-25

- Fix Homebrew upgrades when `brew` is a symlink, including the standard Linuxbrew layout: retain its invocation path while binding and revalidating the resolved manager file.
- Keep the existing exact-formula, receipt, changed-target and no-fallback checks.

## v0.1.0 - 2026-09-25

- Wait for Neovim runtime initialization after connecting RPC, avoiding an intermittent startup failure.

- Publish macOS/Linux amd64/arm64 binary archives, checksums and a filtered source archive.
- Add the personal Homebrew formula channel with generated Bash/Zsh completions.
- Provide `lazymermaid upgrade` and read-only `--check` through verified Homebrew ownership; preserve standalone/local copies and document their external update paths.
- Verify source/module packaging independently; retain embedded resources and development history outside release payloads.
- Distribute the application under MIT.
- Publish a portable module path and source snapshot while keeping local development conversations private.
