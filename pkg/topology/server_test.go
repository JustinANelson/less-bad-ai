package topology

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestTopologyReturnsRedactedBoundedTraceContext(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/app\n")
	mustWrite(t, filepath.Join(root, "pkg", "api", "api.go"), "package api\n")
	prompt := "Use token=super-secret-value to implement the endpoint. " + strings.Repeat("detail ", 100)
	trace := map[string]any{
		"version": 1, "trace_id": "trace-1", "commit_sha": strings.Repeat("a", 40),
		"timestamp": "2026-09-10T12:00:00Z", "user_prompt": prompt,
		"worker_summary": "Called with Authorization: Bearer worker-secret", "tech_lead_modifications": "password=hunter2 removed",
		"touched_files": []string{"pkg/api/api.go"}, "adr_decision": "Accepted.",
	}
	b, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, ".lbai", "traces", "trace.json"), string(b))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3141/api/topology", nil)
	response := httptest.NewRecorder()
	NewServer(root, nil).Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var graph Graph
	if err := json.Unmarshal(response.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	combined := graph.Prompt + graph.Summary + graph.Review
	for _, secret := range []string{"super-secret-value", "worker-secret", "hunter2"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("topology response exposed %q: %#v", secret, graph)
		}
	}
	if !strings.Contains(combined, "[REDACTED]") || len([]rune(graph.Prompt)) > maxPromptDisplay {
		t.Fatalf("context was not redacted and bounded: %#v", graph)
	}
	if graph.Status != "complete" || len(graph.Nodes) != 1 || !graph.Nodes[0].Modified {
		t.Fatalf("trace topology metadata missing: %#v", graph)
	}
}

func TestReadOnlyAPIEndpointsRestrictMethodAndHost(t *testing.T) {
	server := NewServer(t.TempDir(), nil)
	for _, test := range []struct {
		path, method, host string
		want               int
	}{
		{"/api/topology", http.MethodPost, "127.0.0.1:3141", http.StatusMethodNotAllowed},
		{"/api/session", http.MethodPost, "127.0.0.1:3141", http.StatusMethodNotAllowed},
		{"/api/events", http.MethodPost, "127.0.0.1:3141", http.StatusMethodNotAllowed},
		{"/api/session", http.MethodGet, "example.com", http.StatusForbidden},
		{"/api/topology", http.MethodGet, "example.com", http.StatusForbidden},
	} {
		req := httptest.NewRequest(test.method, "http://127.0.0.1:3141"+test.path, nil)
		req.Host = test.host
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		if response.Code != test.want {
			t.Errorf("%s %s host %s: status=%d want=%d", test.method, test.path, test.host, response.Code, test.want)
		}
	}
}

func TestEventsRemovesDisconnectedClient(t *testing.T) {
	server := NewServer(t.TempDir(), nil)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, err := httpServer.Client().Get(httpServer.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "event: ready\n" {
		t.Fatalf("SSE ready event = %q, %v", line, err)
	}
	if clients := server.clientCount(); clients != 1 {
		t.Fatalf("connected client count = %d", clients)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for server.clientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if clients := server.clientCount(); clients != 0 {
		t.Fatalf("disconnected client count = %d", clients)
	}
}

func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func TestSanitizeDisplayTextRedactsSecretsAndTruncatesRunes(t *testing.T) {
	input := "api_key=alpha Authorization: Bearer bravo\npassword='charlie' " + strings.Repeat("\u00e9", 20)
	got := sanitizeDisplayText(input, 50)
	for _, secret := range []string{"alpha", "bravo", "charlie"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitizeDisplayText exposed %q in %q", secret, got)
		}
	}
	if len([]rune(got)) > 50 {
		t.Fatalf("sanitized output has %d runes: %q", len([]rune(got)), got)
	}
}

func TestEmbeddedDashboardUsesLocalSVGRenderer(t *testing.T) {
	b, err := fs.ReadFile(assets, "static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, required := range []string{`<svg id="graph"`, "createElementNS", "renderGraph", "node.modified"} {
		if !strings.Contains(html, required) {
			t.Fatalf("embedded dashboard omitted %q", required)
		}
	}
	for _, forbidden := range []string{"<script src=", "<link rel=\"stylesheet\" href=\"http", "innerHTML"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("embedded dashboard contains forbidden external or unsafe content %q", forbidden)
		}
	}
}
