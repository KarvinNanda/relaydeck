// Command fakeclaude is a stand-in for Claude Code in agent tests. It prints
// READY, echoes each input line as ECHO:<line>, prints SIZE:<cols>x<rows> on
// a window size change (unix), exits 0 on "quit", exits 3 on "fail", asks the
// terminal for the cursor position on "query", and prints "esc to interrupt"
// on "busy".
package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	watchResize()
	fmt.Println("READY")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		switch line {
		case "quit", "/exit":
			os.Exit(0)
		case "fail":
			os.Exit(3)
		case "busy":
			fmt.Print("\x1b[999;1Hesc to interrupt")
		case "query":
			fmt.Print("\x1b[6n")
		}
		fmt.Println("ECHO:" + line)
	}
}
