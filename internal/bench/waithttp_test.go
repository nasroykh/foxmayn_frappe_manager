package bench

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A server that answers is up, whatever it answers with. Frappe returns 404 for
// unknown paths and 301 to the site's canonical host; both are proof of life.
func TestWaitForHTTPAcceptsAnyStatus(t *testing.T) {
	for _, status := range []int{200, 301, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status == 301 {
				w.Header().Set("Location", "https://elsewhere.invalid/")
			}
			w.WriteHeader(status)
		}))
		if err := WaitForHTTP(srv.URL, 3*time.Second); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
		srv.Close()
	}
}

// The regression this function exists for. docker-proxy holds the host-side
// listener for a published container port for as long as the container exists,
// so it accepts the connection and then closes it when nothing inside the
// container is listening. A dial-based wait reports that as a healthy server;
// a real request has to fail.
func TestWaitForHTTPRejectsAnAcceptAndCloseListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// The dial the old implementation did, to show the port really does accept.
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("the listener should accept connections: %v", err)
	}
	conn.Close()

	if err := WaitForHTTP("http://"+ln.Addr().String(), 3*time.Second); err == nil {
		t.Fatal("WaitForHTTP reported a server that never answers a request as up")
	}
}

func TestWaitForHTTPFailsWhenNothingListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := WaitForHTTP("http://"+addr, 3*time.Second); err == nil {
		t.Fatal("WaitForHTTP reported a closed port as up")
	}
}
