//go:build !darwin && !linux

package main

// availableMemory returns 0 on platforms where the memory size is not detected, which keeps the Go defaults.
func availableMemory() uint64 {
	return 0
}
