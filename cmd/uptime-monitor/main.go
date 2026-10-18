// Command uptime-monitor runs the checker, alerting and status page.
//
// Usage:
//
//	uptime-monitor              run the service (configured by env vars)
//	uptime-monitor healthcheck  exit 0 if the local /healthz answers; for
//	                            container health checks in images without curl
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/api"
	"github.com/amlaban65/uptime-monitor/internal/checker"
	"github.com/amlaban65/uptime-monitor/internal/clock"
	"github.com/amlaban65/uptime-monitor/internal/config"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/notify"
	"github.com/amlaban65/uptime-monitor/internal/scheduler"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, 60*time.Second)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := store.Migrate(ctx, db)
	if err != nil {
		return err
	}
	log.Info("database ready", "migrations_applied", applied)

	notifiers := map[string]notify.Notifier{}
	statusURL := cfg.PublicURL + "/"
	if cfg.Slack.Enabled() {
		notifiers["slack"] = notify.NewSlack(cfg.Slack.WebhookURL, statusURL)
	}
	if cfg.SMTP.Enabled() {
		notifiers["email"] = notify.NewEmail(notify.SMTPConfig(cfg.SMTP), statusURL)
	}
	channels := make([]string, 0, len(notifiers))
	for ch := range notifiers {
		channels = append(channels, ch)
	}
	log.Info("alert channels", "channels", channels)

	st := store.New(db, store.WithAlertChannels(channels...), store.WithJitter(cfg.Jitter))

	if cfg.MonitorsFile != "" {
		specs, err := monitor.LoadFile(cfg.MonitorsFile)
		if err != nil {
			return err
		}
		n, err := st.SyncFileMonitors(ctx, specs)
		if err != nil {
			return err
		}
		log.Info("monitors file synced", "path", cfg.MonitorsFile, "monitors", n)
	}

	clk := clock.Real{}
	sched := scheduler.New(st, checker.Default(), clk, scheduler.Config{
		Concurrency:  cfg.Concurrency,
		PollInterval: cfg.PollInterval,
	}, log.With("component", "scheduler"))
	dispatcher := notify.NewDispatcher(st, notifiers, clk, notify.DefaultDispatcherConfig(), log.With("component", "notify"))

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.New(st, cfg.AdminToken, log.With("component", "http")).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sched.Run(ctx) }()
	go func() { defer wg.Done(); dispatcher.Run(ctx) }()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stop()
		wg.Wait()
		return err
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	wg.Wait() // in-flight checks finish, bounded by their timeouts
	log.Info("stopped")
	return nil
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
