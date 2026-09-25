#!/usr/bin/env python3
"""Exercise the compiled TUI and real embedded Neovim in a disposable PTY.

No Python packages, network, Mermaid runtime, or real termaid installation are
required. Only text rendering is stubbed; keyboard routing, Neovim, unsaved
snapshots, resize, quit protection, and terminal restoration are real.

Usage: python3 scripts/pty_smoke.py [--binary ./bin/lazymermaid]
"""

from __future__ import annotations

import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time


CSI = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]")
OSC = re.compile(rb"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
DCS = re.compile(rb"\x1bP.*?\x1b\\", re.S)


def readable(data: bytes) -> str:
    """Remove control strings for transcript assertions, not screen emulation."""
    data = OSC.sub(b"", data)
    data = DCS.sub(b"", data)
    data = CSI.sub(b"", data)
    return data.decode("utf-8", "replace")


class Terminal:
    def __init__(self, command: list[str], cwd: Path, env: dict[str, str]):
        self.master, self.slave = pty.openpty()
        self.before = termios.tcgetattr(self.slave)
        self.output = bytearray()
        self.query_tail = b""
        self.resize(150, 40)

        # Do not make the child own the controlling terminal: macOS revokes the
        # slave when its session leader exits, preventing a post-exit termios
        # restoration check. It still gets real TTY stdin/stdout; resize signals
        # are delivered explicitly below, as they would be by a terminal host.
        self.process = subprocess.Popen(
            command, cwd=cwd, env=env,
            stdin=self.slave, stdout=self.slave, stderr=self.slave,
            start_new_session=True, close_fds=True,
        )

    def resize(self, columns: int, rows: int):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        if hasattr(self, "process") and self.process.poll() is None:
            self.process.send_signal(signal.SIGWINCH)

    def send(self, data: str | bytes):
        if isinstance(data, str):
            data = data.encode("utf-8")
        os.write(self.master, data)

    def pump(self, duration: float = 0.05):
        ready, _, _ = select.select([self.master], [], [], duration)
        if not ready:
            return
        try:
            data = os.read(self.master, 65536)
        except OSError as error:
            if error.errno == errno.EIO:
                return
            raise
        self.output.extend(data)
        # Supply a minimal xterm terminal handshake. Child Neovim's queries are
        # answered by bubbleterm, not here. This only talks to the outer UI.
        pending = self.query_tail + data
        responses = {
            b"\x1b[?2026$p": b"\x1b[?2026;2$y",
            b"\x1b[?2027$p": b"\x1b[?2027;2$y",
            b"\x1b[?u": b"\x1b[?0u",
            b"\x1b[6n": b"\x1b[1;1R",
            b"\x1b[c": b"\x1b[?1;2c",
            b"\x1b[0c": b"\x1b[?1;2c",
            b"\x1b[>0c": b"\x1b[>0;136;0c",
            b"\x1b[>c": b"\x1b[>0;136;0c",
            b"\x1b[>q": b"\x1bP>|lazymermaid-smoke(1.0)\x1b\\",
            b"\x1b]10;?\x1b\\": b"\x1b]10;rgb:ffff/ffff/ffff\x1b\\",
            b"\x1b]11;?\x1b\\": b"\x1b]11;rgb:0000/0000/0000\x1b\\",
        }
        for query, response in responses.items():
            for _ in range(pending.count(query)):
                self.send(response)
            pending = pending.replace(query, b"")
        # Retain only a possible incomplete trailing escape string.
        last_escape = pending.rfind(b"\x1b")
        tail = pending[last_escape:] if last_escape >= 0 else b""
        self.query_tail = tail if any(q.startswith(tail) and q != tail for q in responses) else b""

    def wait(self, predicate, description: str, timeout: float = 8.0):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump()
            if predicate():
                return
            code = self.process.poll()
            if code is not None:
                raise AssertionError(f"app exited ({code}) while waiting for {description}")
        self.process.send_signal(signal.SIGQUIT)
        self.settle(0.5)
        raise AssertionError(f"timed out waiting for {description}")

    def wait_text(self, text: str, start: int = 0, timeout: float = 8.0):
        self.wait(lambda: text in readable(bytes(self.output[start:])), repr(text), timeout)

    def settle(self, duration: float = 0.25):
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            self.pump(min(0.05, max(0.0, deadline-time.monotonic())))

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                if self.process.poll() is None:
                    try:
                        self.process.kill()
                    except ProcessLookupError:
                        pass
                self.process.wait(timeout=3)
        os.close(self.master)
        os.close(self.slave)


def render_sources(path: Path) -> list[str]:
    if not path.exists():
        return []
    sources = []
    for line in path.read_text().splitlines():
        try:
            sources.append(json.loads(line)["source"])
        except (ValueError, KeyError):
            pass  # a currently appending log entry is not complete yet
    return sources


def run(binary: Path, repo: Path):
    with tempfile.TemporaryDirectory(prefix="lazymermaid-pty-smoke-") as temporary:
        work = Path(temporary)
        project = work / "examples"
        shutil.copytree(repo / "examples", project)
        fixture = project / "00-smoke.mmd"
        original = "flowchart LR\n  A --> B[SMOKE_ANCHOR]\n"
        fixture.write_text(original)
        originals = {p: p.read_bytes() for p in project.rglob("*") if p.is_file()}
        log = work / "render.jsonl"
        fake = work / "termaid"
        fake.write_text(
            f"#!{sys.executable}\n"
            "import json, os, sys\n"
            "if '--version' in sys.argv:\n"
            "    print('termaid smoke fixture'); sys.exit(0)\n"
            "source = sys.stdin.read()\n"
            "with open(os.environ['LAZYMERMAID_SMOKE_RENDER_LOG'], 'a') as f:\n"
            "    f.write(json.dumps({'source': source}) + '\\n')\n"
            "print('SMOKE PREVIEW')\n"
            "print(source)\n"
        )
        fake.chmod(0o700)
        config = work / "config.toml"
        config.write_text(
            "[render]\n"
            f"termaid = {json.dumps(str(fake))}\n"
            f"runtime_dir = {json.dumps(str(work / 'absent-official-runtime'))}\n"
            "debounce_ms = 30\n"
            "timeout_seconds = 5\n"
        )
        env = {k: v for k, v in os.environ.items() if not k.startswith("LAZYMERMAID_")}
        for name in ("TMUX", "TMUX_PANE", "KITTY_WINDOW_ID", "WEZTERM_PANE", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "NVIM", "NVIM_LISTEN_ADDRESS"):
            env.pop(name, None)
        env.update({
            "TERM": "xterm-256color", "COLORTERM": "truecolor",
            "XDG_CONFIG_HOME": str(work / "config"),
            "XDG_CACHE_HOME": str(work / "cache"),
            "XDG_DATA_HOME": str(work / "data"),
            "LAZYMERMAID_SMOKE_RENDER_LOG": str(log),
        })
        terminal = Terminal([str(binary), "--config", str(config), "--preview", "unicode", str(project)], work, env)
        try:
            terminal.wait_text("lazymermaid")
            terminal.wait(lambda: bool(render_sources(log)), "first real renderer request")
            terminal.settle(0.3)

            # Printable action keys belong to the filter, not global dispatch.
            start = len(terminal.output)
            terminal.send("/jq/?")
            terminal.wait_text("No diagrams", start)
            assert terminal.process.poll() is None, "typing q in filter quit the app"
            assert "Keyboard help" not in readable(bytes(terminal.output[start:])), "typing ? in filter opened help"
            terminal.send(b"\x1b")
            terminal.settle()
            start = len(terminal.output)
            terminal.send("?")
            terminal.wait_text("Keyboard help", start)
            terminal.send(b"\x1b")
            terminal.settle()

            # Arrow and Vim navigation both remain functional.
            terminal.send(b"\x1b[B\x1b[Akj")
            terminal.settle()
            terminal.send("/SMOKE_ANCHOR")
            terminal.settle()
            terminal.send(b"\r")
            terminal.settle()
            terminal.send("e")
            terminal.settle(0.5)
            terminal.send("Go")
            terminal.settle()
            marker = "中文 e\u0301 👋🏿 jq/?"
            terminal.send("\x1b[200~  B --> C[" + marker + "]\x1b[201~")
            terminal.wait(lambda: any(marker in source for source in render_sources(log)), "unsaved Unicode/combining paste reaching preview")
            assert fixture.read_text() == original, "unsaved editing changed the source file"
            terminal.send(b"\x1b")
            terminal.settle()

            for columns, rows in [(45, 14), (8, 5), (160, 45)]:
                terminal.resize(columns, rows)
                terminal.settle(0.2)
                assert terminal.process.poll() is None, "resize terminated the app"

            # Leave editor using the app escape chord. q is only interpreted by
            # the host outside editor focus, then dirty changes must be guarded.
            terminal.send(b"\x07")
            terminal.settle()
            start = len(terminal.output)
            terminal.send("q")
            terminal.wait_text("Unsaved changes", start)
            terminal.send(b"\x1b")
            terminal.settle()
            assert terminal.process.poll() is None, "canceling dirty quit terminated the app"
            start = len(terminal.output)
            terminal.send("q")
            terminal.wait_text("Unsaved changes", start)
            terminal.send("d")
            terminal.wait(lambda: terminal.process.poll() is not None, "discard-and-quit")
            terminal.settle()
            assert terminal.process.returncode == 0, f"unexpected exit {terminal.process.returncode}"
            assert b"panic:" not in terminal.output, "panic in terminal transcript"
            after = termios.tcgetattr(terminal.slave)
            important_flags = termios.ECHO | termios.ICANON | termios.ISIG
            assert (terminal.before[3] & important_flags) == (after[3] & important_flags), "terminal local modes were not restored"
            assert b"\x1b[?1049l" in terminal.output, "alternate screen was not restored"
            for path, data in originals.items():
                assert path.read_bytes() == data, f"discard modified {path.name}"
            print("PASS: PTY startup, filter typing, help, arrows/Vim navigation, Neovim Unicode paste, unsaved live preview, resize, dirty quit cancel/discard, terminal restoration")
        except Exception:
            with tempfile.NamedTemporaryFile(prefix="lazymermaid-pty-failure-", suffix=".log", delete=False) as failure:
                failure.write(bytes(terminal.output))
                print("Full PTY transcript:", failure.name, file=sys.stderr)
            print("--- Last terminal output (control strings stripped) ---", file=sys.stderr)
            print(readable(bytes(terminal.output[-30000:])), file=sys.stderr)
            print("Raw tail:", repr(bytes(terminal.output[-1000:])), file=sys.stderr)
            raise
        finally:
            terminal.close()


def main():
    repo = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=repo / "bin/lazymermaid")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error(f"compiled app not found: {binary}; build with go build -o bin/lazymermaid ./cmd/lazymermaid")
    if not shutil.which("nvim"):
        parser.error("Neovim is required for this smoke test")
    run(binary, repo)


if __name__ == "__main__":
    main()
