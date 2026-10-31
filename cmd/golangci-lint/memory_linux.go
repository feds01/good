package main

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// availableMemory returns the memory in bytes the process may use, or 0 when unknown:
// the physical memory, lowered to the cgroup limit when there is one.
func availableMemory() uint64 {
	total := physicalMemory()

	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		limit, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			continue
		}

		if total == 0 || limit < total {
			total = limit
		}
	}

	return total
}

func physicalMemory() uint64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}

		kibibytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}

		return kibibytes * 1024
	}

	return 0
}
