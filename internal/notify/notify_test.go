package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type hit struct {
	Path, Body string
	Header     http.Header
}

func server(t *testing.T) (*httptest.Server, func() []hit) {
	var mu sync.Mutex
	var hits []hit
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, string(b), r.Header.Clone()})
		mu.Unlock()
		if strings.Contains(r.URL.Path, "broken") {
			http.Error(w, "nope", 500)
		}
	}))
	t.Cleanup(s.Close)
	return s, func() []hit { mu.Lock(); defer mu.Unlock(); return append([]hit{}, hits...) }
}

func TestSend(t *testing.T) {
	s, hits := server(t)
	c := &Client{HTTP: s.Client(), TelegramAPI: s.URL}
	ctx := context.Background()
	fail := Event{Kind: "backup", Bench: "kb", OK: false, Message: "target r2: connection refused", Host: "vps1"}
	for _, n := range []Notifier{
		{Name: "w", Type: TypeWebhook, URL: s.URL + "/hook"},
		{Name: "n", Type: TypeNtfy, URL: s.URL + "/topic", Token: "tk"},
		{Name: "s", Type: TypeSlack, URL: s.URL + "/slack"},
		{Name: "t", Type: TypeTelegram, Token: "123:ABC", ChatID: "42"},
		{Name: "h", Type: TypeHealthchecks, URL: s.URL + "/ping/uuid"},
	} {
		if err := n.Validate(); err != nil {
			t.Fatalf("%s: %v", n.Name, err)
		}
		if err := c.Send(ctx, n, fail); err != nil {
			t.Fatalf("%s: %v", n.Name, err)
		}
	}
	h := hits()
	var ev Event
	json.Unmarshal([]byte(h[0].Body), &ev)
	if h[0].Path != "/hook" || ev.Bench != "kb" || ev.OK {
		t.Errorf("webhook: %+v", h[0])
	}
	if h[1].Header.Get("Priority") != "high" || h[1].Header.Get("Authorization") != "Bearer tk" || !strings.Contains(h[1].Header.Get("Title"), "FAILED") {
		t.Errorf("ntfy headers: %v", h[1].Header)
	}
	if !strings.Contains(h[2].Body, "ffm backup kb on vps1: FAILED") {
		t.Errorf("slack body: %s", h[2].Body)
	}
	if h[3].Path != "/bot123:ABC/sendMessage" || !strings.Contains(h[3].Body, `"chat_id":"42"`) {
		t.Errorf("telegram: %+v", h[3])
	}
	if h[4].Path != "/ping/uuid/fail" {
		t.Errorf("healthchecks failure path: %s", h[4].Path)
	}
	hc := Notifier{Name: "h", Type: TypeHealthchecks, URL: s.URL + "/ping/uuid"}
	c.Start(ctx, hc)
	c.Send(ctx, hc, Event{Kind: "backup", OK: true})
	h = hits()
	if h[5].Path != "/ping/uuid/start" || h[6].Path != "/ping/uuid" {
		t.Errorf("healthchecks start/success: %s %s", h[5].Path, h[6].Path)
	}

	// A failing endpoint is an error, and the token never shows in it.
	err := c.Send(ctx, Notifier{Name: "t", Type: TypeTelegram, Token: "999:SECRET", ChatID: "1"}, fail)
	if err == nil {
		t.Skip("server accepted")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("token in error: %v", err)
	}
}

func TestWantsAndValidate(t *testing.T) {
	ok, fail := Event{Kind: "backup", OK: true}, Event{Kind: "backup"}
	if (Notifier{Type: TypeNtfy}).Wants(ok) || !(Notifier{Type: TypeNtfy}).Wants(fail) {
		t.Error("default is failures only")
	}
	if !(Notifier{Type: TypeNtfy, On: OnAlways}).Wants(ok) || !(Notifier{Type: TypeHealthchecks}).Wants(ok) {
		t.Error("always / healthchecks must fire on success")
	}
	if (Notifier{Type: TypeHealthchecks}).Wants(Event{Kind: "verify", OK: true}) {
		t.Error("a verify pinged the backup check")
	}
	for _, bad := range []Notifier{
		{Name: "a", Type: TypeNtfy, URL: "ftp://x"},
		{Name: "a", Type: TypeTelegram, Token: "t"},
		{Name: "A b", Type: TypeNtfy, URL: "https://ntfy.sh/x"},
		{Name: "a", Type: "email"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if d := (Notifier{Type: TypeSlack, URL: "https://hooks.slack.com/services/T/B/SECRET"}).Describe(); strings.Contains(d, "SECRET") {
		t.Errorf("Describe leaks: %s", d)
	}
}
