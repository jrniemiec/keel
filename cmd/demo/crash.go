package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	crashAt = flag.Int("crash-at", 0, "exit before the Nth model or tool call (0 = never)")
	calls   int // model and tool calls made by this process
)

// maybeCrash simulates a crash: the process exits before the Nth call runs.
func maybeCrash(what string) {
	calls++
	if calls == *crashAt {
		fmt.Printf("crash before call %d (%s)\n", calls, what)
		os.Exit(1)
	}
}
