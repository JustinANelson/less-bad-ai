package topology

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/jnels/less-bad-ai/pkg/memory"
)

//go:embed static/index.html
var assets embed.FS

type Server struct {
	Root, Token string
	Undo        func() error
	mu          sync.Mutex
	transaction sync.Mutex
	clients     map[chan string]struct{}
}

func NewServer(root string, undo func() error) *Server {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &Server{Root: root, Token: hex.EncodeToString(b), Undo: undo, clients: map[chan string]struct{}{}}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/topology", s.topology)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/revert", s.revert)
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"token": s.Token}) })
	static, _ := fs.Sub(assets, "static")
	mux.Handle("/", http.FileServer(http.FS(static)))
	return mux
}
func (s *Server) topology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	var modified []string
	var prompt, summary string
	if traces, listErr := memory.List(s.Root, 1); listErr == nil && len(traces) == 1 {
		modified, prompt, summary = traces[0].TouchedFiles, traces[0].UserPrompt, traces[0].WorkerSummary
	}
	g, err := Build(s.Root, modified)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	g.Prompt, g.Summary = prompt, summary
	writeJSON(w, g)
}
func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
		http.Error(w, "cross-origin request rejected", 403)
		return
	}
	if r.Header.Get("X-LBAI-Token") != s.Token {
		http.Error(w, "unauthorized", 401)
		return
	}
	if s.Undo == nil {
		http.Error(w, "undo unavailable", 503)
		return
	}
	s.transaction.Lock()
	defer s.transaction.Unlock()
	if err := s.Undo(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Broadcast("reverted")
	writeJSON(w, map[string]string{"status": "reverted"})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	ch := make(chan string, 4)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.clients, ch); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, "event: ready\ndata: connected\n\n")
	flusher.Flush()
	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "event: update\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
func (s *Server) Broadcast(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Host, host) {
		return false
	}
	hostname := u.Hostname()
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
