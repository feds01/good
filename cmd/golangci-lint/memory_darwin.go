package main

import (
	"encoding/binary"
	"syscall"
)

const uint64Size = 8

// availableMemory returns the physical memory in bytes, or 0 when unknown.
func availableMemory() uint64 {
	value, err := syscall.Sysctl("hw.memsize")
	if err != nil {
		return 0
	}

	// syscall.Sysctl drops the trailing zero byte of the 8-byte value.
	raw := make([]byte, uint64Size)
	copy(raw, value)

	return binary.LittleEndian.Uint64(raw)
}
