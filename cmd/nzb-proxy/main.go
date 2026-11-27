package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cornfeedhobo/nzb-proxy/internal/app"
)

// Release builds set these with Go linker flags. Ordinary local builds report dev.
var version = "dev"
var commit = "unknown"

func main() {
	showVersion := flag.Bool("version", false, "print version and commit, then exit")
	healthcheck := flag.Bool("healthcheck", false, "check the local HTTP server on port 8080, then exit")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *showVersion {
		fmt.Printf("nzb-proxy %s (commit %s)\n", version, commit)
		return
	}
	if *healthcheck {
		client := http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			slog.Error("health check failed", "error", err)
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slog.Error("health check failed", "status", resp.StatusCode)
			os.Exit(1)
		}
		return
	}
	if err := app.Run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
