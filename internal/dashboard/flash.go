package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// flashTTL bounds how long an unread flash message is kept.
const flashTTL = 5 * time.Minute

type flash struct {
	ok, err string
	expires time.Time
}

// flashStore holds one-shot messages shown after a redirect, keyed by a
// random id that is the only thing put in the URL.
type flashStore struct {
	mu sync.Mutex
	m  map[string]flash
}

var flashes = &flashStore{m: make(map[string]flash)}

func (f *flashStore) put(ok, err string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	f.m[id] = flash{ok: ok, err: err, expires: now.Add(flashTTL)}
	return id
}

// take returns the messages for id and forgets them.
func (f *flashStore) take(id string) (ok, err string) {
	if id == "" {
		return "", ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, found := f.m[id]
	delete(f.m, id)
	if !found || time.Now().After(v.expires) {
		return "", ""
	}
	return v.ok, v.err
}
