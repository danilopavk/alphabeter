// Package email sends the app's emails (verification, password reset).
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Message struct {
	From    string // "Name <address>"
	To      string
	Subject string
	Text    string
	HTML    string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Resend sends through Resend's HTTP API (Railway's Hobby plan blocks SMTP).
type Resend struct {
	APIKey  string
	BaseURL string // defaults to https://api.resend.com; tests point it elsewhere
	Client  *http.Client
}

func (r *Resend) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return "https://api.resend.com"
}

func (r *Resend) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]any{
		"from": m.From, "to": []string{m.To}, "subject": m.Subject, "text": m.Text, "html": m.HTML,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL()+"/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("email: resend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("email: resend returned %d: %s", resp.StatusCode, detail)
	}
	return nil
}

// Log prints emails instead of sending them (local development).
type Log struct{}

func (Log) Send(_ context.Context, m Message) error {
	slog.Info("email not sent (no RESEND_API_KEY)", "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}

// Recorder keeps sent emails in memory, for tests.
type Recorder struct {
	mu   sync.Mutex
	sent []Message
}

func (r *Recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

// Last returns the most recent email sent to the address.
func (r *Recorder) Last(to string) (Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sent) - 1; i >= 0; i-- {
		if r.sent[i].To == to {
			return r.sent[i], true
		}
	}
	return Message{}, false
}
