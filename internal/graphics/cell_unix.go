//go:build darwin || linux || freebsd || openbsd || netbsd

package graphics

import (
	"golang.org/x/sys/unix"
	"io"
)

// TIOCGWINSZ is a read-only kernel query, not a second terminal input reader.
func terminalCellSize(out io.Writer) (int, int) {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return 0, 0
	}
	size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil || size.Col == 0 || size.Row == 0 || size.Xpixel == 0 || size.Ypixel == 0 {
		return 0, 0
	}
	return int(size.Xpixel) / int(size.Col), int(size.Ypixel) / int(size.Row)
}
