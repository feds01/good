//golangcitest:args -Erevive
//golangcitest:expected_exitcode 0
// Package testdata ...
package testdata

/*
 #include <stdlib.h>
*/
import "C"

import "unsafe"

// Free releases a C string.
func Free(s *C.char) {
	C.free(unsafe.Pointer(s))
}
