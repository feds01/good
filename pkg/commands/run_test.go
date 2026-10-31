package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_computeCgoSalt(t *testing.T) {
	for _, name := range []string{"CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CC", "CXX"} {
		t.Setenv(name, "")
	}

	assert.Empty(t, computeCgoSalt())

	t.Setenv("CGO_CFLAGS", "-DFOO=1")

	assert.Equal(t, "CGO_CFLAGS=-DFOO=1;", computeCgoSalt())

	t.Setenv("CC", "clang")

	assert.Equal(t, "CGO_CFLAGS=-DFOO=1;CC=clang;", computeCgoSalt())
}
