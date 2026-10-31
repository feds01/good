// Package dep is a cgo package that imports nothing else, so nothing that it imports pulls in syscall or runtime/cgo.
package dep

/*
static int twice(int v) {
	return 2 * v;
}
*/
import "C"

func Twice(value int) int {
	return int(C.twice(C.int(value)))
}
