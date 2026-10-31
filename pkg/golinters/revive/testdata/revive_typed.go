//golangcitest:args -Erevive
// Package testdata ...
package testdata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var timeoutSecs time.Duration // want "time-naming: var timeoutSecs is of type time.Duration; don't use unit-specific suffix \"Secs\""

var errFailed error = errors.New("failed") // want "var-declaration: should omit type error from declaration of var errFailed; it will be inferred from the right-hand side"

var interval time.Duration = time.Second // want "var-declaration: should omit type time.Duration from declaration of var interval; it will be inferred from the right-hand side"

func testContextKey() context.Context {
	return context.WithValue(context.Background(), "key", 1) // want "context-keys-type: should not use basic type string as key in context.WithValue"
}

func testErrorf(t *testing.T) {
	t.Error(fmt.Sprintf("failed after %d", 1)) // want "errorf: should replace t.Error\\(fmt.Sprintf\\(...\\)\\) with t.Errorf\\(...\\)"
}
