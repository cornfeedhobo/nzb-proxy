package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/cornfeedhobo/nzb-proxy/internal/app"
)

// Release builds set these with Go linker flags. Ordinary local builds report dev.
var version = "dev"
var commit = "unknown"

func main() {
	showVersion := flag.Bool("version", false, "print version and commit, then exit")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *showVersion {
		fmt.Printf("nzb-proxy %s (commit %s)\n", version, commit)
		return
	}
	if err := app.Run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
