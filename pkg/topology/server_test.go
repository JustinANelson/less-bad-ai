package topology

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevertRequiresPostLoopbackOriginAndToken(t *testing.T) {
	var calls atomic.Int32
	server := NewServer(t.TempDir(), func() error { calls.Add(1); return nil })
	handler := server.Handler()
	tests := []struct {
		name, method, origin, token string
		want                        int
	}{
		{name: "get", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "cross origin", method: http.MethodPost, origin: "http://evil.example", token: server.Token, want: http.StatusForbidden},
		{name: "spoofed host", method: http.MethodPost, origin: "http://evil.example", token: server.Token, want: http.StatusForbidden},
		{name: "missing token", method: http.MethodPost, origin: "http://127.0.0.1:3141", want: http.StatusUnauthorized},
		{name: "authorized", method: http.MethodPost, origin: "http://127.0.0.1:3141", token: server.Token, want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "http://127.0.0.1:3141/api/revert", nil)
			if test.name == "spoofed host" {
				req.Host = "evil.example"
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			req.Header.Set("X-LBAI-Token", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatalf("undo calls = %d, want 1", calls.Load())
	}
}

func TestRevertSerializesUndo(t *testing.T) {
	var active, maximum atomic.Int32
	server := NewServer(t.TempDir(), func() error {
		current := active.Add(1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return nil
	})
	handler := server.Handler()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3141/api/revert", nil)
			req.Header.Set("Origin", "http://127.0.0.1:3141")
			req.Header.Set("X-LBAI-Token", server.Token)
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent undo calls = %d", maximum.Load())
	}
}

func TestSameOriginAcceptsOnlyMatchingLoopbackHosts(t *testing.T) {
	for _, test := range []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:3141", "127.0.0.1:3141", true},
		{"http://localhost:3141", "localhost:3141", true},
		{"http://[::1]:3141", "[::1]:3141", true},
		{"http://example.com", "example.com", false},
		{"not a URL", "127.0.0.1:3141", false},
	} {
		if got := sameOrigin(test.origin, test.host); got != test.want {
			t.Errorf("sameOrigin(%q, %q) = %t, want %t", test.origin, test.host, got, test.want)
		}
	}
}
