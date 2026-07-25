package main

import (
	"os"

	agentcmd "github.com/liuchong/lark-agent/agent/cmd"
)

func main() {
	os.Exit(agentcmd.Execute(os.Stdin, os.Stdout, os.Stderr, os.Args[1:]))
}
