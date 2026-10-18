package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig configures the email notifier.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
}

// Email sends alerts over SMTP, upgrading with STARTTLS when the server
// offers it.
type Email struct {
	cfg       SMTPConfig
	statusURL string
}

func NewEmail(cfg SMTPConfig, statusURL string) *Email {
	return &Email{cfg: cfg, statusURL: statusURL}
}

func (m *Email) Notify(ctx context.Context, e Event) error {
	err := m.send(ctx, e)
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) && tpErr.Code >= 500 {
		return Permanent(err) // 5xx: rejected recipient, auth failure, etc.
	}
	return err
}

func (m *Email) send(ctx context.Context, e Event) error {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	// net/smtp has no context support, so bound the whole session with a
	// connection deadline instead.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return err
	}

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection to anything but localhost.
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return err
	}
	for _, to := range m.cfg.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.message(e)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (m *Email) message(e Event) []byte {
	var b strings.Builder
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", m.cfg.From)
	header("To", strings.Join(m.cfg.To, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", e.Subject()))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(e.Body(m.statusURL), "\n", "\r\n"))
	return []byte(b.String())
}
