//go:build !windows

package server

import "syscall"

const addrInUseErrno = syscall.EADDRINUSE
