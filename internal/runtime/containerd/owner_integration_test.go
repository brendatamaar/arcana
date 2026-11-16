//go:build linux && integration

package containerd

import (
	"io/fs"
	"syscall"
)

func fileOwner(info fs.FileInfo) int { return int(info.Sys().(*syscall.Stat_t).Uid) }
