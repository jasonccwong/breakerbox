// breakerbox-agent runs on each host, supervises registered apps, collects
// metrics, and maintains an outbound WebSocket to the hub. It exposes no
// inbound network surface and executes only the fixed app-scoped verb set.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Printf("breakerbox-agent %s\n", version)
	case "run", "enroll", "apps":
		fmt.Fprintf(os.Stderr, "breakerbox-agent %s: not implemented yet (Phase 1)\n", os.Args[1])
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `breakerbox-agent — the BreakerBox host agent

Usage:
  breakerbox-agent run                          run the agent (service entrypoint)
  breakerbox-agent enroll --hub URL --token T   enroll this host with a hub
  breakerbox-agent apps list                    list registered apps and approval state
  breakerbox-agent apps approve <id>            approve an app definition on this host
  breakerbox-agent apps reject <id>             reject an app definition
  breakerbox-agent apps import <file>           import breakerbox.app.json (register + approve)
  breakerbox-agent version                      print version`)
}
