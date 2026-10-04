package main

import (
	"log/slog"
	"os"

	"github.com/cornfeedhobo/nzb-proxy/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
