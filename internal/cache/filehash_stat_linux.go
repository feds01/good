//go:build linux

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
		ModNano:    st.Mtim.Nano(),
		ChangeNano: st.Ctim.Nano(),
		Inode:      st.Ino,
		Device:     uint64(st.Dev), //nolint:unconvert // The type of Dev depends on the architecture.
	}, true
}
