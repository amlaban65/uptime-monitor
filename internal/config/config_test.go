package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.Concurrency != 32 || c.PollInterval != time.Second || c.Jitter != 0.1 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Slack.Enabled() || c.SMTP.Enabled() {
		t.Fatal("notifiers should be disabled by default")
	}
}

func TestLoadNotifiers(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DATABASE_URL":      "postgres://x",
		"SLACK_WEBHOOK_URL": "https://hooks.slack.com/services/T/B/X",
		"SMTP_HOST":         "mail",
		"SMTP_FROM":         "a@example.com",
		"SMTP_TO":           "b@example.com, c@example.com",
		"PUBLIC_URL":        "https://status.example.com/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Slack.Enabled() || !c.SMTP.Enabled() || len(c.SMTP.To) != 2 || c.SMTP.To[1] != "c@example.com" {
		t.Fatalf("unexpected notifier config: %+v", c)
	}
	if c.PublicURL != "https://status.example.com" {
		t.Fatalf("PublicURL = %q", c.PublicURL)
	}
}

func TestLoadCollectsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"CHECK_CONCURRENCY": "zero",
		"CHECK_JITTER":      "2",
		"RESULT_RETENTION":  "24h",
		"SLACK_WEBHOOK_URL": "http://insecure",
		"SMTP_HOST":         "mail",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"DATABASE_URL", "CHECK_CONCURRENCY", "CHECK_JITTER", "RESULT_RETENTION", "SLACK_WEBHOOK_URL", "SMTP_FROM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
