// Command manual-eval-agent is a deterministic coding-agent stand-in used by
// scripts/manual-compare.ps1. Its first pass makes a buildable architectural
// violation; an LBAI correction prompt makes it repair that violation.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	prompt := strings.Join(os.Args[1:], " ")
	if strings.Contains(prompt, "Automated Tech Lead") {
		fmt.Println("Reviewed the corrected ports-and-adapters implementation; no further edits required.")
		return
	}
	if strings.Contains(prompt, "verification failed") {
		mustWrite("internal/app/fetch.go", correctedApp)
		mustWrite("internal/adapters/httpfetch/client.go", adapter)
		mustWrite("internal/adapters/httpfetch/client_test.go", correctedTest)
		_ = os.Remove(filepath.FromSlash("internal/app/fetch_test.go"))
		fmt.Println("Moved HTTP access behind an injected application interface and added an adapter.")
		return
	}
	mustWrite("internal/app/fetch.go", violatingApp)
	mustWrite("internal/app/fetch_test.go", violatingTest)
	_ = os.Remove(filepath.FromSlash("internal/adapters/httpfetch/client.go"))
	fmt.Println("Added a direct HTTP-backed FetchTitle implementation and tests.")
}

func mustWrite(path, content string) {
	path = filepath.FromSlash(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

const violatingApp = `package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func FetchTitle(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil { return "", err }
	resp, err := http.DefaultClient.Do(req)
	if err != nil { return "", err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return "", fmt.Errorf("unexpected status: %s", resp.Status) }
	body, err := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body)), err
}
`

const violatingTest = `package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "  example title  ") }))
	defer server.Close()
	got, err := FetchTitle(context.Background(), server.URL)
	if err != nil || got != "example title" { t.Fatalf("FetchTitle() = %q, %v", got, err) }
}
`

const correctedApp = `package app

import "context"

type TextFetcher interface {
	Fetch(context.Context, string) (string, error)
}

func FetchTitle(ctx context.Context, endpoint string, fetcher TextFetcher) (string, error) {
	return fetcher.Fetch(ctx, endpoint)
}
`

const adapter = `package httpfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct { HTTPClient *http.Client }

func (c Client) Fetch(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil { return "", err }
	client := c.HTTPClient
	if client == nil { client = http.DefaultClient }
	resp, err := client.Do(req)
	if err != nil { return "", err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { return "", fmt.Errorf("unexpected status: %s", resp.Status) }
	body, err := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body)), err
}
`

const correctedTest = `package httpfetch_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/lbai-manual-comparison/internal/adapters/httpfetch"
)

func TestFetchTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "  example title  ") }))
	defer server.Close()
	got, err := (httpfetch.Client{}).Fetch(context.Background(), server.URL)
	if err != nil || got != "example title" { t.Fatalf("Fetch() = %q, %v", got, err) }
}
`
