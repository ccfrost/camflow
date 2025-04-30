//go:build darwin
// +build darwin

package commands

/*
#cgo CFLAGS: -Wall
#include <copyfile.h> // copyfile
#include <stdlib.h> // C.free
#include <errno.h>  // errno

// Define a struct to hold return value and errno from the C call.
typedef struct {
    int ret_val; // The return value from copyfile (-1 on error, 0 on success)
    int err_no;  // The value of errno if ret_val is -1
} copyfile_result_t;

// Wrapper function in C: Calls copyfile and captures errno if needed.
// Created this because I don't see how to access C.errno directly in Go.
// Using static inline helps the C compiler optimize.
static inline copyfile_result_t call_copyfile_and_get_errno(const char *src, const char *dst, copyfile_state_t state, copyfile_flags_t flags) {
    copyfile_result_t res;
    res.ret_val = copyfile(src, dst, state, flags);
    if (res.ret_val == -1) {
        res.err_no = errno;
    } else {
        res.err_no = 0;
    }
    return res;
}
*/
import "C"

import (
	"fmt"
	"syscall" // Provides Errno type wrapping C's errno.
	"unsafe"  // Required for C.free with C.CString.
)

// copyFileMacOS wraps the macOS C copyfile function.
func copyFileMacOS(src, dst string) error {
	cSrc := C.CString(src)
	defer C.free(unsafe.Pointer(cSrc))

	cDst := C.CString(dst)
	defer C.free(unsafe.Pointer(cDst))

	// Pass nil for the state argument for simple cases.
	if ret := C.call_copyfile_and_get_errno(cSrc, cDst, nil, C.COPYFILE_ALL); ret.ret_val == -1 {
		return fmt.Errorf("C.copyfile failed for src='%s', dst='%s': %w",
			src, dst, syscall.Errno(ret.err_no))
	}
	return nil
}
