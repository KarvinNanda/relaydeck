//go:build !unix

package agent

import "os"

func killTree(p *os.Process) error { return p.Kill() }
