//go:build darwin

package cache

import (
	"os"
	"syscall"
)

const isStatIdentitySupported = true

func statIdentity(info os.FileInfo) (fileStat, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{}, false
	}

	return fileStat{
		Size:       st.Size,
		ModNano:    st.Mtimespec.Nano(),
		ChangeNano: st.Ctimespec.Nano(),
		Inode:      st.Ino,
		Device:     uint64(st.Dev), //nolint:gosec // The number is only compared for equality.
	}, true
}
