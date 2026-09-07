//go:build linux || darwin || freebsd

package fileserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/roadrunner-server/config/v6"
	"github.com/roadrunner-server/tcplisten"
)

type testLogger struct{}

func (testLogger) NamedLogger(string) *slog.Logger { return slog.New(slog.DiscardHandler) }

func fileConfig(t *testing.T, path, data string) *config.Plugin {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Plugin{Path: path}
	if err := cfg.Init(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestUnixSocket(t *testing.T) {
	t.Chdir(t.TempDir())
	const content = "UNIX file server\n"
	if err := os.WriteFile("file.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	t.Setenv("RR_TEST_SOCKET_UID", strconv.Itoa(uid))
	t.Setenv("RR_TEST_SOCKET_GID", strconv.Itoa(gid))
	const data = `version: '3'
fileserver:
  address: unix://files.sock
  unix_socket: {mode: "0640", uid: "${RR_TEST_SOCKET_UID}", gid: "${RR_TEST_SOCKET_GID}"}
  serve: [{prefix: /, root: .}]
`
	cfg := fileConfig(t, ".rr.yaml", data)
	p := &Plugin{}
	if err := p.Init(cfg, testLogger{}); err != nil {
		t.Fatal(err)
	}
	errCh := p.Serve()
	stop := sync.OnceValue(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.Stop(ctx)
	})
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", "files.sock")
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/file.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != content {
		t.Fatalf("unexpected response: status=%d body=%q", resp.StatusCode, body)
	}
	info, err := os.Stat("files.sock")
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o640 || int64(stat.Uid) != int64(uid) || int64(stat.Gid) != int64(gid) {
		t.Fatalf("unexpected socket attributes: mode=%v uid=%d gid=%d", info.Mode(), stat.Uid, stat.Gid)
	}
	transport.CloseIdleConnections()
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat("files.sock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after stop: %v", err)
	}
	select {
	case err = <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestUnixSocketFileConfig(t *testing.T) {
	t.Setenv("RR_TEST_SOCKET_ID", "33")
	t.Setenv("RR_TEST_SOCKET_MODE", "0640")
	zero, uid, gid := 0, 33, 34
	cases := []struct {
		name    string
		format  string
		address string
		options string
		want    *tcplisten.UnixSocketOptions
		wantErr string
	}{
		{name: "absent TCP", address: "127.0.0.1:10101"},
		{name: "absent UNIX", address: "unix://test.sock"},
		{name: "empty UNIX", address: "unix://test.sock", options: "{}"},
		{name: "empty TCP", address: "127.0.0.1:10101", options: "{}"},
		{name: "empty TCP scheme", address: "tcp://127.0.0.1:10101", options: "{}"},
		{name: "empty address", options: "{}", wantErr: "empty address"},
		{name: "TCP options", address: "127.0.0.1:10101", options: `{"mode":"0600"}`, wantErr: "filesystem unix:// address"},
		{name: "invalid mode", address: "unix://test.sock", options: `{"mode":"0780"}`, wantErr: "invalid unix socket mode"},
		{name: "negative UID", address: "unix://test.sock", options: `{"uid":-1}`, wantErr: "invalid unix socket uid"},
		{name: "negative GID", address: "unix://test.sock", options: `{"gid":-1}`, wantErr: "invalid unix socket gid"},
		{name: "reserved UID", address: "unix://test.sock", options: `{"uid":4294967295}`, wantErr: "invalid unix socket uid"},
		{name: "reserved GID", address: "unix://test.sock", options: `{"gid":4294967295}`, wantErr: "invalid unix socket gid"},
		{name: "mode only", address: "unix://test.sock", options: `{"mode":"0600"}`, want: &tcplisten.UnixSocketOptions{Mode: "0600"}},
		{name: "zero values", address: "unix://test.sock", options: `{"mode":"0000","uid":0,"gid":0}`, want: &tcplisten.UnixSocketOptions{Mode: "0000", UID: &zero, GID: &zero}},
		{name: "null IDs", address: "unix://test.sock", options: `{"uid":null,"gid":null}`, want: &tcplisten.UnixSocketOptions{}},
		{name: "environment values", address: "unix://test.sock", options: `{"mode":"${RR_TEST_SOCKET_MODE}","uid":"${RR_TEST_SOCKET_ID}","gid":"${RR_TEST_SOCKET_ID}"}`, want: &tcplisten.UnixSocketOptions{Mode: "0640", UID: &uid, GID: &uid}},
		{name: "integral IDs", format: "json", address: "unix://test.sock", options: `{"uid":33.0,"gid":34.0}`, want: &tcplisten.UnixSocketOptions{UID: &uid, GID: &gid}},
		{name: "empty TCP", format: "json", address: "127.0.0.1:10101", options: "{}"},
		{name: "empty UNIX", format: "json", address: "unix://test.sock", options: "{}"},
	}
	for _, tc := range cases {
		format := cmp.Or(tc.format, "yaml")
		t.Run(format+"/"+tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			data := fmt.Sprintf(`version: "3"
fileserver:
  address: %q
  serve: [{prefix: /}]
`, tc.address)
			if tc.options != "" {
				data += "  unix_socket: " + tc.options + "\n"
			}
			if format == "json" {
				block := ""
				if tc.options != "" {
					block = `,"unix_socket":` + tc.options
				}
				data = fmt.Sprintf(`{"version":"3","fileserver":{"address":%q,"serve":[{"prefix":"/"}]%s}}`, tc.address, block)
			}
			path := ".rr." + format
			cfg := fileConfig(t, path, data)
			p := &Plugin{}
			err := p.Init(cfg, testLogger{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.want, p.config.UnixSocket) {
				t.Fatalf("expected socket options %+v, got %+v", tc.want, p.config.UnixSocket)
			}
		})
	}
}

func TestUnixSocketOwnershipError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("Requires an unprivileged process.")
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	otherGID := 0
	for otherGID == os.Getegid() || slices.Contains(groups, otherGID) {
		otherGID++
	}

	for _, tc := range []struct {
		field string
		id    int
	}{
		{field: "uid", id: 0},
		{field: "gid", id: otherGID},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("RR_TEST_SOCKET_ID", strconv.Itoa(tc.id))
			data := fmt.Sprintf(`version: "3"
fileserver:
  address: unix://files.sock
  unix_socket: {%s: "${RR_TEST_SOCKET_ID}"}
  serve: [{prefix: /, root: .}]
`, tc.field)
			p := &Plugin{}
			if err := p.Init(fileConfig(t, ".rr.yaml", data), testLogger{}); err != nil {
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
		})
	}
}
