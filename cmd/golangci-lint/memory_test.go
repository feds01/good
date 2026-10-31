//go:build darwin || linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_availableMemory(t *testing.T) {
	assert.Greater(t, availableMemory(), uint64(gib/2))
}
