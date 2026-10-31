package cache

import "time"

// fileStat is the part of the file metadata that changes whenever the content of the file does.
type fileStat struct {
	Size       int64
	ModNano    int64
	ChangeNano int64
	Inode      uint64
	Device     uint64
}

// latestChange returns the most recent of the modification time and the change time.
func (s fileStat) latestChange() time.Time {
	return time.Unix(0, max(s.ModNano, s.ChangeNano))
}
