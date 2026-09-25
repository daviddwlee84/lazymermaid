# Neovim RPC connects before the editor runtime is ready

Symptom: `initialize Neovim: nvim:nvim_exec_lua exception: Lua: ... attempt to
index global 'lazymermaid' (a nil value)` during startup, intermittently on macOS.

Neovim's `--listen` socket can service RPC while `init.lua` yields inside native
startup commands. A successful `nvim.Dial` proves transport availability, not
that the application runtime table/functions or VimEnter have completed.

`editor.New` now probes the final runtime function and `vim.v.vim_did_enter`
from separate RPC requests before connecting handlers. The existing startup
budget, caller cancellation and child-exit checks still apply. Do not block the
RPC with a Lua-side wait that could prevent the interrupted initialization from
resuming; return between probes.

`TestRealNeovimWaitsForYieldingInitialization` prefixes the owned init with a
short `vim.wait`, forcing early RPC availability. It uses real Neovim and
private temporary paths. The existing unsaved-Unicode, virtual-save and terminal
lifecycle tests exercise the completed editor afterward.

Found by the macOS job in [CI run 36095319236](https://github.com/daviddwlee84/lazymermaid/actions/runs/36095319236)
on 2026-09-25. No user Neovim configuration was changed.
