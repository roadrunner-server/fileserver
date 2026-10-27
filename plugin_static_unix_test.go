//go:build linux || darwin || freebsd

package fileserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStaticFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	const content = "Hello, World!"
	compressedContent := strings.Repeat(content, 100)
	for _, file := range []struct {
		name    string
		content string
	}{
		{name: "file.txt", content: content},
		{name: "hello world.txt", content: content},
		{name: "compressed.txt", content: compressedContent},
	} {
		if err := os.WriteFile(file.name, []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name          string
		method        string
		path          string
		headers       http.Header
		calculateEtag bool
		weak          bool
		compress      bool
		wantStatus    int
		wantBody      string
		wantHeaders   map[string]string
	}{
		{
			name: "prefix and cache control", method: http.MethodGet, path: "/assets/file.txt",
			wantStatus: http.StatusOK, wantBody: content,
			wantHeaders: map[string]string{"Cache-Control": "public, max-age=60", "Etag": ""},
		},
		{
			name: "trailing slash prefix", method: http.MethodGet, path: "/download/file.txt",
			wantStatus: http.StatusOK, wantBody: content,
		},
		{
			name: "encoded filename", method: http.MethodGet, path: "/assets/hello%20world.txt",
			wantStatus: http.StatusOK, wantBody: content,
		},
		{
			name: "head", method: http.MethodHead, path: "/assets/file.txt",
			wantStatus: http.StatusOK, wantHeaders: map[string]string{"Content-Length": "13"},
		},
		{
			name: "byte range", method: http.MethodGet, path: "/assets/file.txt",
			headers:    http.Header{"Range": {"bytes=0-4"}},
			wantStatus: http.StatusPartialContent, wantBody: "Hello",
			wantHeaders: map[string]string{"Content-Range": "bytes 0-4/13"},
		},
		{
			name: "stale range validator", method: http.MethodGet, path: "/assets/file.txt",
			headers:    http.Header{"Range": {"bytes=0-4"}, "If-Range": {"Wed, 21 Oct 2015 07:28:00 GMT"}},
			wantStatus: http.StatusOK, wantBody: content,
		},
		{
			name: "prefix boundary", method: http.MethodGet, path: "/assetsfile.txt",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "missing file", method: http.MethodGet, path: "/assets/missing.txt",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "directory browsing disabled", method: http.MethodGet, path: "/assets/",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "strong etag", method: http.MethodGet, path: "/assets/file.txt", calculateEtag: true,
			wantStatus: http.StatusOK, wantBody: content,
			wantHeaders: map[string]string{"Etag": `"13-1831710635"`},
		},
		{
			name: "weak etag", method: http.MethodGet, path: "/assets/file.txt", calculateEtag: true, weak: true,
			wantStatus: http.StatusOK, wantBody: content,
			wantHeaders: map[string]string{"Etag": `W/"13-1831710635"`},
		},
		{
			name: "etag revalidation", method: http.MethodGet, path: "/assets/file.txt", calculateEtag: true,
			headers:    http.Header{"If-None-Match": {`"13-1831710635"`}},
			wantStatus: http.StatusNotModified,
		},
		{
			name: "compression enabled", method: http.MethodGet, path: "/assets/compressed.txt", compress: true,
			wantStatus: http.StatusOK, wantBody: compressedContent,
		},
		{
			name: "compression disabled", method: http.MethodGet, path: "/assets/compressed.txt",
			wantStatus: http.StatusOK, wantBody: compressedContent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serveFileserver(t, fmt.Sprintf(`version: '3'
fileserver:
  address: unix://files.sock
  calculate_etag: %t
  weak: %t
  serve:
    - {prefix: /assets, root: ., bytes_range: true, compress: %t, max_age: 60}
    - {prefix: /download/, root: .}
`, tc.calculateEtag, tc.weak, tc.compress))
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", "files.sock")
			}}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			req, err := http.NewRequestWithContext(t.Context(), tc.method, "http://localhost"+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header = tc.headers.Clone()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("expected HTTP %d, got %d", tc.wantStatus, resp.StatusCode)
			}
			for key, want := range tc.wantHeaders {
				if got := resp.Header.Get(key); got != want {
					t.Errorf("expected %s %q, got %q", key, want, got)
				}
			}
			if resp.Uncompressed != tc.compress {
				t.Errorf("expected gzip compression %t, got %t", tc.compress, resp.Uncompressed)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus != http.StatusNotFound && string(body) != tc.wantBody {
				t.Fatalf("expected body %q, got %q", tc.wantBody, body)
			}
		})
	}
}
