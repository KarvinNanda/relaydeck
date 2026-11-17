//go:build windows

package server

import "syscall"

// WSAEADDRINUSE. syscall.EADDRINUSE on windows is an invented value that
// a real bind error never matches.
const addrInUseErrno = syscall.Errno(10048)
