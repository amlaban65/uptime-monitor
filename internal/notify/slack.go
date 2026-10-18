package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Slack posts to a Slack incoming webhook.
type Slack struct {
	webhookURL string
	statusURL  string
	client     *http.Client
}

func NewSlack(webhookURL, statusURL string) *Slack {
	return &Slack{webhookURL: webhookURL, statusURL: statusURL, client: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Slack) Notify(ctx context.Context, e Event) error {
	payload, err := json.Marshal(map[string]string{
		"text": "*" + e.Subject() + "*\n" + e.Body(s.statusURL),
	})
	if err != nil {
		return Permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return Permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err // network errors are worth retrying
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	err = fmt.Errorf("slack webhook: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	// 4xx means the request or webhook is bad and will stay bad, except
	// for rate limiting.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return Permanent(err)
	}
	return err
}
