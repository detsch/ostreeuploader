//go:build linux

package ostree

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestTransportHeaders verifies per-request headers are sent on every GET.
// Headers are fiopull's only auth mechanism (e.g. a bearer token supplied by
// the caller); there is no built-in mTLS/gateway handling.
func TestTransportHeaders(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen["Authorization"] = r.Header.Get("Authorization")
		seen["X-Correlation-ID"] = r.Header.Get("X-Correlation-ID")
		mu.Unlock()
		http.Error(w, "nf", http.StatusNotFound) // content doesn't matter
	}))
	defer srv.Close()

	tr, err := newTransport(RemoteConfig{
		BaseURL: srv.URL,
		Headers: map[string]string{"Authorization": "Bearer tok123", "X-Correlation-ID": "corr-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := tr.open(context.Background(), "refs/heads/main", 0)
	if rc != nil {
		rc.Close()
	}
	if !isNotFound(err) {
		t.Fatalf("expected 404, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["Authorization"] != "Bearer tok123" {
		t.Errorf("Authorization not sent: %q", seen["Authorization"])
	}
	if seen["X-Correlation-ID"] != "corr-9" {
		t.Errorf("X-Correlation-ID not sent: %q", seen["X-Correlation-ID"])
	}
}
