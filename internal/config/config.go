// Package config loads runtime configuration from environment variables.
// Secrets (database password, webhook URL, SMTP password, admin token) only
// ever come from the environment, never from files in the repo.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration.
type Config struct {
	DatabaseURL  string
	ListenAddr   string
	AdminToken   string // bearer token for the management API; empty disables writes
	MonitorsFile string // optional YAML file of monitors synced at startup
	PublicURL    string // used for links in alerts
	PageTitle    string

	Concurrency  int           // max checks in flight per replica
	PollInterval time.Duration // how often a replica looks for due checks
	Jitter       float64       // fraction of the interval added at random to each run
	Retention    time.Duration // raw check results older than this are deleted

	Slack SlackConfig
	SMTP  SMTPConfig
}

type SlackConfig struct {
	WebhookURL string
}

func (c SlackConfig) Enabled() bool { return c.WebhookURL != "" }

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
}

func (c SMTPConfig) Enabled() bool { return c.Host != "" }

// Load reads configuration using getenv (os.Getenv in production; a map in
// tests) and validates it.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	c := Config{
		DatabaseURL:  p.str("DATABASE_URL", ""),
		ListenAddr:   p.str("LISTEN_ADDR", ":8080"),
		AdminToken:   p.str("ADMIN_TOKEN", ""),
		MonitorsFile: p.str("MONITORS_FILE", ""),
		PublicURL:    strings.TrimRight(p.str("PUBLIC_URL", "http://localhost:8080"), "/"),
		PageTitle:    p.str("STATUS_PAGE_TITLE", "Service Status"),
		Concurrency:  p.int("CHECK_CONCURRENCY", 32),
		PollInterval: p.duration("POLL_INTERVAL", time.Second),
		Jitter:       p.float("CHECK_JITTER", 0.1),
		Retention:    p.duration("RESULT_RETENTION", 35*24*time.Hour),
		Slack:        SlackConfig{WebhookURL: p.str("SLACK_WEBHOOK_URL", "")},
		SMTP: SMTPConfig{
			Host:     p.str("SMTP_HOST", ""),
			Port:     p.int("SMTP_PORT", 587),
			Username: p.str("SMTP_USERNAME", ""),
			Password: p.str("SMTP_PASSWORD", ""),
			From:     p.str("SMTP_FROM", ""),
			To:       p.list("SMTP_TO"),
		},
	}
	errs := p.errs
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.Concurrency < 1 {
		errs = append(errs, errors.New("CHECK_CONCURRENCY must be at least 1"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("POLL_INTERVAL must be positive"))
	}
	if c.Jitter < 0 || c.Jitter > 1 {
		errs = append(errs, errors.New("CHECK_JITTER must be between 0 and 1"))
	}
	// The status page reports 30-day uptime from raw results.
	if c.Retention < 31*24*time.Hour {
		errs = append(errs, errors.New("RESULT_RETENTION must be at least 744h (31 days)"))
	}
	if c.Slack.Enabled() {
		if u, err := url.Parse(c.Slack.WebhookURL); err != nil || u.Scheme != "https" {
			errs = append(errs, errors.New("SLACK_WEBHOOK_URL must be an https URL"))
		}
	}
	if c.SMTP.Enabled() && (c.SMTP.From == "" || len(c.SMTP.To) == 0) {
		errs = append(errs, errors.New("SMTP_FROM and SMTP_TO are required when SMTP_HOST is set"))
	}
	return c, errors.Join(errs...)
}

type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

func (p *parser) int(key string, def int) int {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", key, err))
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", key, err))
	}
	return f
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("%s: %w", key, err))
	}
	return d
}

func (p *parser) list(key string) []string {
	var out []string
	for _, s := range strings.Split(p.getenv(key), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
