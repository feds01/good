//go:build !darwin && !linux

package cache

import "os"

// Without a change time the memo cannot notice an edit that restores the modification time.
const isStatIdentitySupported = false

func statIdentity(os.FileInfo) (fileStat, bool) {
	return fileStat{}, false
}
