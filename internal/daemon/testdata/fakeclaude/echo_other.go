//go:build !(darwin || freebsd || netbsd || openbsd || linux)

package main

func rawControlEcho() {}
