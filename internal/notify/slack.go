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

// SlackSink posts a message to a Slack incoming webhook (§5.8, v1's one sink).
type SlackSink struct {
	WebhookURL string
	HTTPClient *http.Client // nil means a client with defaultHTTPTimeout
}

// defaultHTTPTimeout guards against a hung webhook host blocking a
// reconcile worker or the debounce transaction's held row lock (see
// maybeNotify) indefinitely — http.DefaultClient has no timeout at all.
const defaultHTTPTimeout = 10 * time.Second

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

func (s *SlackSink) Send(ctx context.Context, message string) error {
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: message})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.HTTPClient
	if client == nil {
		client = defaultHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send slack webhook: %w", err)
	}
	defer func() {
		// Drain before Close: the transport can only put the underlying
		// connection back in its keep-alive pool if the body was read to
		// EOF first — closing early forces a fresh connection (and a new
		// TLS handshake) on every subsequent Send.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}
