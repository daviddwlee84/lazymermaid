# Blank TUI with only terminal control sequences

Observed while running the full application in a real PTY.

## Symptom

No visible first frame, although Neovim/RPC and the Bubble Tea event loop are
alive. The output contains controls such as `?1049h`, cursor hide, and clear
screen. A goroutine dump showed the main event loop waiting normally.

## Cause and fix

The image presenter wrapped stdout as only an `io.Writer`. Bubble Tea v2 needs
its output to implement the terminal `File` interface (`Read`, `Write`, `Close`,
`Fd`) to detect the terminal and obtain window size. Without forwarding it, the
renderer uses a zero-size surface.

The presenter now forwards `Read` and `Fd` to the actual terminal. Its `Close`
cleans only owned image resources and does not close the user's stdout.

## Prevention

Keep the graphics terminal-forwarding tests and `scripts/pty_smoke.py`. Unit
model snapshots cannot catch failure of the actual terminal descriptor boundary.
