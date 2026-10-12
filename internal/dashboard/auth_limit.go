package dashboard

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Failed-login throttling. The dashboard has one shared password and no
// session, so without this anyone who can reach the port can guess it at
// network speed.
//
// Rules, keyed on the TCP peer address only (X-Forwarded-For is
// attacker-controlled; behind a reverse proxy every client shares the proxy's
// address and so shares one budget, see the README):
//   - authFailsPerWindow wrong passwords inside authWindow lock that address
//     out for authBackoffBase, doubling on each further lockout up to
//     authBackoffMax. A correct login or an hour without failures resets it.
//   - authGlobalFails wrong passwords from all addresses inside authWindow
//     lock everyone out until the window drains, so rotating source addresses
//     (an IPv6 /64) does not defeat the per-address limit.
//   - a request with no credentials at all is the browser's first probe, not a
//     guess, and is never counted.
const (
	authFailsPerWindow = 5
	authGlobalFails    = 30
	authWindow         = time.Minute
	authBackoffBase    = 30 * time.Second
	authBackoffMax     = 15 * time.Minute
	authIdleReset      = time.Hour
	authSweepAbove     = 1024
)

type ipAuthState struct {
	fails        []time.Time
	strikes      int
	blockedUntil time.Time
	lastFail     time.Time
}

type authLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	ips    map[string]*ipAuthState
	global []time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, ips: map[string]*ipAuthState{}}
}

// prune drops timestamps older than the window.
func prune(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= authWindow {
		i++
	}
	return ts[i:]
}

// check reports whether ip may attempt authentication now. When it may not,
// retry says how long to wait.
func (l *authLimiter) check(ip string) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.global = prune(l.global, now)
	if len(l.global) >= authGlobalFails {
		return false, authWindow - now.Sub(l.global[0])
	}
	if st := l.ips[ip]; st != nil && now.Before(st.blockedUntil) {
		return false, st.blockedUntil.Sub(now)
	}
	return true, 0
}

// fail records a wrong password from ip.
func (l *authLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.ips) > authSweepAbove {
		l.sweep(now)
	}
	l.global = append(prune(l.global, now), now)
	st := l.ips[ip]
	if st == nil {
		st = &ipAuthState{}
		l.ips[ip] = st
	}
	if !st.lastFail.IsZero() && now.Sub(st.lastFail) > authIdleReset {
		st.strikes = 0
	}
	st.lastFail = now
	st.fails = append(prune(st.fails, now), now)
	if len(st.fails) >= authFailsPerWindow {
		d := authBackoffBase << st.strikes
		if d > authBackoffMax || d <= 0 {
			d = authBackoffMax
		}
		st.strikes++
		st.blockedUntil = now.Add(d)
		st.fails = nil
	}
}

// success clears ip's record.
func (l *authLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
}

func (l *authLimiter) sweep(now time.Time) {
	for ip, st := range l.ips {
		if now.After(st.blockedUntil) && now.Sub(st.lastFail) > authIdleReset {
			delete(l.ips, ip)
		}
	}
}

// clientIP is the TCP peer, deliberately ignoring forwarding headers.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
