// Package notify tells people when a scheduled backup or a check fails (or
// succeeds): webhook, ntfy, Telegram, Slack, and healthchecks.io-style
// dead-man pings, which alert when a run stops arriving at all.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Types of notifier.
const (
	TypeWebhook      = "webhook"
	TypeNtfy         = "ntfy"
	TypeTelegram     = "telegram"
	TypeSlack        = "slack"
	TypeHealthchecks = "healthchecks"
)

// When a notifier fires.
const (
	OnFailure = "failure"
	OnAlways  = "always"
)

// Notifier is one configured channel.
type Notifier struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// URL is the webhook / ntfy topic / Slack webhook / healthchecks ping URL.
	URL string `json:"url,omitempty"`
	// Token is the Telegram bot token, or an ntfy access token.
	Token  string `json:"token,omitempty"`
	ChatID string `json:"chat_id,omitempty"`
	// On is OnFailure (default) or OnAlways. Healthchecks pings always fire:
	// a missing success ping is the alert.
	On string `json:"on,omitempty"`
}

// Event is something worth telling.
type Event struct {
	Kind    string    `json:"kind"` // "backup", "verify", "doctor"
	Bench   string    `json:"bench,omitempty"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Host    string    `json:"host,omitempty"`
	At      time.Time `json:"at"`
}

// Title is a one-line summary.
func (e Event) Title() string {
	status := "OK"
	if !e.OK {
		status = "FAILED"
	}
	subject := e.Kind
	if e.Bench != "" {
		subject += " " + e.Bench
	}
	if e.Host != "" {
		subject += " on " + e.Host
	}
	return fmt.Sprintf("ffm %s: %s", subject, status)
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Validate checks that a notifier is complete.
func (n Notifier) Validate() error {
	if !nameRe.MatchString(n.Name) {
		return fmt.Errorf("notifier name %q: lowercase letters, digits and '-', up to 32", n.Name)
	}
	if n.On != "" && n.On != OnFailure && n.On != OnAlways {
		return fmt.Errorf("--on must be %s or %s", OnFailure, OnAlways)
	}
	needURL := func() error {
		u, err := url.Parse(n.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("notifier %q needs an http(s) URL", n.Name)
		}
		return nil
	}
	switch n.Type {
	case TypeWebhook, TypeNtfy, TypeSlack, TypeHealthchecks:
		return needURL()
	case TypeTelegram:
		if n.Token == "" || n.ChatID == "" {
			return fmt.Errorf("notifier %q (telegram) needs a bot token and --chat-id", n.Name)
		}
		return nil
	}
	return fmt.Errorf("notifier type %q: must be webhook, ntfy, telegram, slack or healthchecks", n.Type)
}

// Wants reports whether the notifier fires for an event. A healthchecks
// check stands for the scheduled backups alone: a successful verify or
// doctor run pinging it would hide backups that stopped.
func (n Notifier) Wants(e Event) bool {
	if n.Type == TypeHealthchecks {
		return e.Kind == "backup" || e.Kind == "test"
	}
	return n.On == OnAlways || !e.OK
}

// Describe is a secret-free summary: the host of the URL only.
func (n Notifier) Describe() string {
	on := n.On
	if on == "" {
		on = OnFailure
	}
	if n.Type == TypeHealthchecks {
		on = "every run"
	}
	where := ""
	if u, err := url.Parse(n.URL); err == nil && u.Host != "" {
		where = " " + u.Host
	}
	if n.Type == TypeTelegram {
		where = " chat " + n.ChatID
	}
	return fmt.Sprintf("%s%s (%s)", n.Type, where, on)
}

// Client sends notifications; HTTP is replaceable in tests.
type Client struct {
	HTTP *http.Client
	// TelegramAPI is the Bot API base, overridable in tests.
	TelegramAPI string
}

// Default is a client with a 20 s timeout.
var Default = &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, TelegramAPI: "https://api.telegram.org"}

func (c *Client) post(ctx context.Context, u, contentType string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return redact(err, u)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// redact keeps a URL (which often carries the secret) out of an error.
func redact(err error, u string) error {
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), u, "<url>"))
}

// Send delivers one event through one notifier.
func (c *Client) Send(ctx context.Context, n Notifier, e Event) error {
	text := e.Title() + "\n" + e.Message
	switch n.Type {
	case TypeWebhook:
		raw, _ := json.Marshal(e)
		return c.post(ctx, n.URL, "application/json", raw, nil)
	case TypeNtfy:
		h := map[string]string{"Title": e.Title(), "Tags": "floppy_disk"}
		if !e.OK {
			h["Priority"], h["Tags"] = "high", "warning"
		}
		if n.Token != "" {
			h["Authorization"] = "Bearer " + n.Token
		}
		return c.post(ctx, n.URL, "text/plain", []byte(e.Message), h)
	case TypeSlack:
		raw, _ := json.Marshal(map[string]string{"text": text})
		return c.post(ctx, n.URL, "application/json", raw, nil)
	case TypeTelegram:
		raw, _ := json.Marshal(map[string]string{"chat_id": n.ChatID, "text": text})
		u := c.TelegramAPI + "/bot" + n.Token + "/sendMessage"
		err := c.post(ctx, u, "application/json", raw, nil)
		if err != nil {
			return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), n.Token, "<token>"))
		}
		return nil
	case TypeHealthchecks:
		u := strings.TrimSuffix(n.URL, "/")
		if !e.OK {
			u += "/fail"
		}
		return c.post(ctx, u, "text/plain", []byte(text), nil)
	}
	return fmt.Errorf("unknown notifier type %q", n.Type)
}

// Start tells a healthchecks notifier a run has begun, so a run that hangs
// is reported as such. Other types ignore it.
func (c *Client) Start(ctx context.Context, n Notifier) error {
	if n.Type != TypeHealthchecks {
		return nil
	}
	return c.post(ctx, strings.TrimSuffix(n.URL, "/")+"/start", "text/plain", nil, nil)
}
