//golangcitest:args -Erevive
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
