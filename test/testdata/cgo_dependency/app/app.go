// Package app uses a cgo dependency, which is not analyzed itself.
package app

import (
	"fmt"

	"github.com/golangci/golangci-lint/v2/test/testdata/cgo_dependency/dep"
)

func Example() {
	fmt.Printf("bad format %d", "text")
	fmt.Println(dep.Twice(2))
}
