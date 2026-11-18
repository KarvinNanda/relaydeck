//go:build !unix

package main

func termSize() string { return "unknown" }
