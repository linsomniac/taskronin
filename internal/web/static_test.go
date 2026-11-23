package web_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/web"
)

func TestCSSSelfContained(t *testing.T) {
	data, err := web.StaticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := strings.ToLower(string(data))
	for _, forbidden := range []string{"@import", "url(", "http:", "https:", "expression("} {
		if strings.Contains(css, forbidden) {
			t.Errorf("app.css contains %q", forbidden)
		}
	}
}

func TestHTMXAssetPinned(t *testing.T) {
	b, err := web.StaticFS.ReadFile("static/" + web.HTMXAsset)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != web.HTMXSize {
		t.Errorf("htmx size = %d, want %d", len(b), web.HTMXSize)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != web.HTMXSHA256 || len(web.HTMXSHA256) != 64 {
		t.Errorf("htmx SHA-256 = %s, want %s", got, web.HTMXSHA256)
	}
	checksums, err := web.StaticFS.ReadFile("static/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	if want := web.HTMXSHA256 + "  " + web.HTMXAsset + "\n"; string(checksums) != want {
		t.Errorf("SHA256SUMS = %q, want %q", checksums, want)
	}
}

func TestStaticServing(t *testing.T) {
	h := newHandler(t, "127.0.0.1:7878")
	for _, tc := range []struct{ name, contentType string }{
		{web.HTMXAsset, "text/javascript; charset=utf-8"},
		{"app.css", "text/css; charset=utf-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/"+tc.name, nil)
			r.Host = "127.0.0.1:7878"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
			if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Errorf("Cache-Control = %q", got)
			}
			b, err := web.StaticFS.ReadFile("static/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(w.Body.Bytes(), b) {
				t.Error("served body differs from embedded bytes")
			}
		})
	}
	for _, path := range []string{"/static/SHA256SUMS", "/static/", "/static/nope.js", "/static/../web.go"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			r.Host = "127.0.0.1:7878"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if path == "/static/../web.go" {
				if w.Code < 300 || w.Code >= 400 || w.Header().Get("Location") != "/web.go" {
					t.Errorf("path cleaning response = %d %v, want redirect to /web.go", w.Code, w.Header())
				}
			} else if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
			if bytes.Contains(w.Body.Bytes(), []byte("package web")) {
				t.Error("static handler exposed Go source")
			}
		})
	}
}
