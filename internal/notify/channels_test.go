package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var sampleEvent = Event{
	IncidentID:  9,
	Kind:        EventResolved,
	MonitorName: "checkout\r\nBcc: attacker@example.com",
	Target:      "https://shop.example.com/health",
	StartedAt:   time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	ResolvedAt:  ptr(time.Date(2026, 1, 1, 10, 7, 30, 0, time.UTC)),
	Cause:       "status 502, expected 200",
}

func ptr[T any](v T) *T { return &v }

func TestEventText(t *testing.T) {
	if s := sampleEvent.Subject(); strings.ContainsAny(s, "\r\n") || !strings.HasPrefix(s, "[RESOLVED]") {
		t.Fatalf("subject %q", s)
	}
	body := sampleEvent.Body("https://status.example.com")
	for _, want := range []string{"down for 7m30s", "status 502", "https://status.example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestSlack(t *testing.T) {
	var got map[string]string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := NewSlack(srv.URL, "https://status.example.com")

	if err := s.Notify(context.Background(), sampleEvent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got["text"], "[RESOLVED]") {
		t.Fatalf("payload %v", got)
	}

	status = http.StatusInternalServerError
	if err := s.Notify(context.Background(), sampleEvent); err == nil || IsPermanent(err) {
		t.Fatalf("500 should be retryable, got %v", err)
	}
	status = http.StatusTooManyRequests
	if err := s.Notify(context.Background(), sampleEvent); err == nil || IsPermanent(err) {
		t.Fatalf("429 should be retryable, got %v", err)
	}
	status = http.StatusNotFound
	if err := s.Notify(context.Background(), sampleEvent); !IsPermanent(err) {
		t.Fatalf("404 should be permanent, got %v", err)
	}
}

func TestEmail(t *testing.T) {
	srv := startFakeSMTP(t, "")
	e := NewEmail(SMTPConfig{Host: "127.0.0.1", Port: srv.port, From: "alerts@example.com", To: []string{"ops@example.com", "dev@example.com"}}, "")
	if err := e.Notify(context.Background(), sampleEvent); err != nil {
		t.Fatal(err)
	}
	msg := <-srv.messages
	if msg.from != "alerts@example.com" || len(msg.rcpts) != 2 {
		t.Fatalf("envelope from=%s rcpts=%v", msg.from, msg.rcpts)
	}
	headers, _, _ := strings.Cut(msg.data, "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("monitor name injected a header:\n%s", headers)
		}
	}
	if !strings.Contains(msg.data, "Subject: [RESOLVED]") || !strings.Contains(msg.data, "down for 7m30s") {
		t.Fatalf("unexpected message:\n%s", msg.data)
	}
}

func TestEmailRejectedRecipientIsPermanent(t *testing.T) {
	srv := startFakeSMTP(t, "550 5.1.1 no such user")
	e := NewEmail(SMTPConfig{Host: "127.0.0.1", Port: srv.port, From: "a@example.com", To: []string{"nobody@example.com"}}, "")
	if err := e.Notify(context.Background(), sampleEvent); !IsPermanent(err) {
		t.Fatalf("got %v, want permanent error", err)
	}
}

func TestEmailConnectionRefusedIsRetryable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	e := NewEmail(SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@example.com", To: []string{"b@example.com"}}, "")
	if err := e.Notify(context.Background(), sampleEvent); err == nil || IsPermanent(err) {
		t.Fatalf("got %v, want retryable error", err)
	}
}

type smtpMessage struct {
	from  string
	rcpts []string
	data  string
}

type fakeSMTP struct {
	port     int
	messages chan smtpMessage
}

// startFakeSMTP speaks just enough SMTP for net/smtp. If rcptReply is set,
// RCPT TO is answered with it.
func startFakeSMTP(t *testing.T, rcptReply string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{port: ln.Addr().(*net.TCPAddr).Port, messages: make(chan smtpMessage, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		var msg smtpMessage
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimRight(line, "\r\n")
			upper := strings.ToUpper(cmd)
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				reply("250-fake")
				reply("250 8BITMIME")
			case strings.HasPrefix(upper, "MAIL FROM:"):
				msg.from = strings.Trim(cmd[len("MAIL FROM:"):], "<> ")
				if i := strings.Index(msg.from, ">"); i >= 0 {
					msg.from = msg.from[:i]
				}
				reply("250 ok")
			case strings.HasPrefix(upper, "RCPT TO:"):
				if rcptReply != "" {
					reply(rcptReply)
					continue
				}
				msg.rcpts = append(msg.rcpts, strings.Trim(cmd[len("RCPT TO:"):], "<> "))
				reply("250 ok")
			case upper == "DATA":
				reply("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				msg.data = b.String()
				reply("250 queued as " + strconv.Itoa(len(msg.data)))
				f.messages <- msg
			case upper == "QUIT":
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return f
}
