//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package graphics

import "io"

func terminalCellSize(out io.Writer) (int, int) { return 0, 0 }
