// Command uptime-monitor runs the checker, alerting and status page.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/amlaban65/uptime-monitor/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	log.Info("configuration loaded", "listen", cfg.ListenAddr)
}
