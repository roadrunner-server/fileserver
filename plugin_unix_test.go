//go:build linux || darwin || freebsd

package fileserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/roadrunner-server/config/v6"
)

type testLogger struct{}

func (testLogger) NamedLogger(string) *slog.Logger { return slog.New(slog.DiscardHandler) }

func fileConfig(t *testing.T, data string) *config.Plugin {
	t.Helper()
	if err := os.WriteFile(".rr.yaml", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Plugin{Path: ".rr.yaml"}
	if err := cfg.Init(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func serveFileserver(t *testing.T, data string) <-chan error {
	t.Helper()
	p := &Plugin{}
	if err := p.Init(fileConfig(t, data), testLogger{}); err != nil {
		t.Fatal(err)
	}
	errCh := p.Serve()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return errCh
}

func getUnixFile(t *testing.T, path string) *http.Response {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", "files.sock")
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUnixSocketServesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	const content = "UNIX file server\n"
	if err := os.WriteFile("file.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	serveFileserver(t, `version: '3'
fileserver:
  address: unix://files.sock
  unix_socket: {mode: "0600"}
  serve: [{prefix: /, root: .}]
`)
	resp := getUnixFile(t, "/file.txt")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if string(body) != content {
		t.Fatalf("expected body %q, got %q", content, body)
	}
}

func TestUnixSocketAttributes(t *testing.T) {
	t.Chdir(t.TempDir())
	uid, gid := os.Getuid(), os.Getgid()
	serveFileserver(t, fmt.Sprintf(`version: '3'
fileserver:
  address: unix://files.sock
  unix_socket: {mode: "0640", uid: %d, gid: %d}
  serve: [{prefix: /, root: .}]
`, uid, gid))
	resp := getUnixFile(t, "/")
	defer resp.Body.Close()

	info, err := os.Stat("files.sock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected mode 0640, got %04o", info.Mode().Perm())
	}
	stat := info.Sys().(*syscall.Stat_t)
	if int64(stat.Uid) != int64(uid) || int64(stat.Gid) != int64(gid) {
		t.Fatalf("expected uid=%d gid=%d, got uid=%d gid=%d", uid, gid, stat.Uid, stat.Gid)
	}
}

func TestUnixSocketStopRemovesListener(t *testing.T) {
	t.Chdir(t.TempDir())
	// Cleanup runs in reverse order, so the server stops before this check.
	t.Cleanup(func() {
		if _, err := os.Stat("files.sock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket remains after stop: %v", err)
		}
	})
	serveFileserver(t, `version: '3'
fileserver:
  address: unix://files.sock
  unix_socket: {mode: "0600"}
  serve: [{prefix: /, root: .}]
`)
	resp := getUnixFile(t, "/")
	defer resp.Body.Close()
}

func TestUnixSocketInitRejectsInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		options string
		wantErr string
	}{
		{name: "TCP options", address: "127.0.0.1:10101", options: `{mode: "0600"}`, wantErr: "filesystem unix:// address"},
		{name: "invalid mode", address: "unix://test.sock", options: `{mode: "0780"}`, wantErr: "invalid unix socket mode"},
		{name: "negative UID", address: "unix://test.sock", options: `{uid: -1}`, wantErr: "invalid unix socket uid"},
		{name: "negative GID", address: "unix://test.sock", options: `{gid: -1}`, wantErr: "invalid unix socket gid"},
		{name: "reserved UID", address: "unix://test.sock", options: `{uid: 4294967295}`, wantErr: "invalid unix socket uid"},
		{name: "reserved GID", address: "unix://test.sock", options: `{gid: 4294967295}`, wantErr: "invalid unix socket gid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			data := fmt.Sprintf(`version: "3"
fileserver:
  address: %q
  unix_socket: %s
  serve: [{prefix: /, root: .}]
`, tc.address, tc.options)
			p := &Plugin{}
			err := p.Init(fileConfig(t, data), testLogger{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestUnixSocketOwnershipErrorRemovesListener(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("Requires an unprivileged process.")
	}
	t.Chdir(t.TempDir())
	errCh := serveFileserver(t, `version: "3"
fileserver:
  address: unix://files.sock
  unix_socket: {uid: 0}
  serve: [{prefix: /, root: .}]
`)
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "chown unix socket") {
			t.Fatalf("expected an ownership error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected an ownership error")
	}
	if _, err := os.Stat("files.sock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after failure: %v", err)
	}
}
