# Pitfalls

Verified past failures, indexed by observable symptom. Record the symptom,
root cause, fix and prevention when a non-obvious problem is likely to recur.
Preserve exact error messages where available, and link to existing documentation
rather than duplicating it.

Current usage belongs in docs/; deferred investigation belongs in backlog/.
These files are maintainer documentation and are not embedded in the binary.

| Symptom | Cause and prevention |
| --- | --- |
| [Blank TUI with only control sequences](blank-tui-with-control-sequences.md) | An output wrapper hid the terminal File interface; retain forwarding and real PTY tests |
