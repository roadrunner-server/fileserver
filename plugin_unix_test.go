//go:build linux || darwin || freebsd

package fileserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/roadrunner-server/config/v6"
	"github.com/roadrunner-server/tcplisten"
)

type testConfig struct {
	cfg    *Config
	socket map[string]any
}

func (c testConfig) Has(name string) bool {
	return name == pluginName || name == "fileserver.unix_socket" && c.socket != nil
}

func (c testConfig) UnmarshalKey(name string, out any) error {
	if name == "fileserver.unix_socket" {
		*out.(*map[string]any) = c.socket
		return nil
	}
	*out.(**Config) = c.cfg
	return nil
}

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
	info, err := os.Stat("files.sock")
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o640 || int64(stat.Uid) != int64(uid) || int64(stat.Gid) != int64(gid) {
		t.Fatalf("unexpected socket attributes: mode=%v uid=%d gid=%d", info.Mode(), stat.Uid, stat.Gid)
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

func TestUnixSocketRawIDs(t *testing.T) {
	type namedID int64
	for _, field := range []string{"uid", "gid"} {
		for _, tc := range []struct {
			value   any
			invalid bool
		}{
			{value: nil},
			{value: int(0)},
			{value: int8(33)},
			{value: int16(33)},
			{value: int32(33)},
			{value: int64(4294967294)},
			{value: namedID(33)},
			{value: uint(0)},
			{value: uint8(33)},
			{value: uint16(33)},
			{value: uint32(33)},
			{value: uint64(4294967294)},
			{value: uintptr(33)},
			{value: "0"},
			{value: "0x21"},
			{value: float32(33)},
			{value: float64(33)},
			{value: int64(-1), invalid: true},
			{value: int64(4294967295), invalid: true},
			{value: uint64(4294967295), invalid: true},
			{value: float64(4294967295), invalid: true},
			{value: float64(1.9), invalid: true},
			{value: float32(-0.5), invalid: true},
			{value: math.NaN(), invalid: true},
			{value: math.Inf(1), invalid: true},
			{value: math.Inf(-1), invalid: true},
			{value: true, invalid: true},
			{value: false, invalid: true},
			{value: "", invalid: true},
			{value: "4294967295", invalid: true},
			{value: "1.9", invalid: true},
			{value: []int{33}, invalid: true},
			{value: map[string]any{}, invalid: true},
		} {
			t.Run(fmt.Sprintf("%s/%T/%v", field, tc.value, tc.value), func(t *testing.T) {
				cfg := testConfig{
					cfg:    &Config{Address: "unix://test.sock", Configuration: []*Cfg{{Prefix: "/"}}},
					socket: map[string]any{field: tc.value},
				}
				p := &Plugin{}
				err := p.Init(cfg, testLogger{})
				if p.app != nil {
					t.Fatal("server started during configuration")
				}
				if tc.invalid {
					if err == nil || !strings.Contains(err.Error(), "fileserver.unix_socket."+field) {
						t.Fatalf("expected a field error, got %v", err)
					}
					if p.config != nil {
						t.Fatal("invalid raw ID reached typed decoding")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestUnixSocketFileConfig(t *testing.T) {
	t.Setenv("RR_TEST_SOCKET_ID", "33")
	t.Setenv("RR_TEST_SOCKET_MODE", "0640")
	t.Setenv("RR_TEST_SOCKET_MISSING", "")
	if err := os.Unsetenv("RR_TEST_SOCKET_MISSING"); err != nil {
		t.Fatal(err)
	}
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
		{name: "empty UNIX", address: "unix://test.sock", options: "{}", want: &tcplisten.UnixSocketOptions{}},
		{name: "empty TCP", address: "127.0.0.1:10101", options: "{}", wantErr: "fileserver.unix_socket"},
		{name: "empty TCP scheme", address: "tcp://127.0.0.1:10101", options: "{}", wantErr: "fileserver.unix_socket"},
		{name: "empty address", options: "{}", wantErr: "empty address"},
		{name: "mode only", address: "unix://test.sock", options: `{"mode":"0600"}`, want: &tcplisten.UnixSocketOptions{Mode: "0600"}},
		{name: "zero values", address: "unix://test.sock", options: `{"mode":"0000","uid":0,"gid":0}`, want: &tcplisten.UnixSocketOptions{Mode: "0000", UID: &zero, GID: &zero}},
		{name: "null IDs", address: "unix://test.sock", options: `{"uid":null,"gid":null}`, want: &tcplisten.UnixSocketOptions{}},
		{name: "environment values", address: "unix://test.sock", options: `{"mode":"${RR_TEST_SOCKET_MODE}","uid":"${RR_TEST_SOCKET_ID}","gid":"${RR_TEST_SOCKET_ID}"}`, want: &tcplisten.UnixSocketOptions{Mode: "0640", UID: &uid, GID: &uid}},
		{name: "missing environment", address: "unix://test.sock", options: `{"uid":"${RR_TEST_SOCKET_MISSING}"}`, wantErr: "fileserver.unix_socket.uid"},
		{name: "boolean UID", address: "unix://test.sock", options: `{"uid":true}`, wantErr: "fileserver.unix_socket.uid"},
		{name: "boolean GID", address: "unix://test.sock", options: `{"gid":false}`, wantErr: "fileserver.unix_socket.gid"},
		{name: "fractional UID", address: "unix://test.sock", options: `{"uid":1.9}`, wantErr: "fileserver.unix_socket.uid"},
		{name: "negative fractional GID", address: "unix://test.sock", options: `{"gid":-0.5}`, wantErr: "fileserver.unix_socket.gid"},
		{name: "integral IDs", format: "json", address: "unix://test.sock", options: `{"uid":33.0,"gid":34.0}`, want: &tcplisten.UnixSocketOptions{UID: &uid, GID: &gid}},
		{name: "fractional UID", format: "json", address: "unix://test.sock", options: `{"uid":1.9}`, wantErr: "fileserver.unix_socket.uid"},
		{name: "boolean GID", format: "json", address: "unix://test.sock", options: `{"gid":false}`, wantErr: "fileserver.unix_socket.gid"},
		{name: "empty TCP", format: "json", address: "127.0.0.1:10101", options: "{}", wantErr: "fileserver.unix_socket"},
		{name: "empty UNIX", format: "json", address: "unix://test.sock", options: "{}", want: &tcplisten.UnixSocketOptions{}},
	}
	for _, tc := range cases {
		format := cmp.Or(tc.format, "yaml")
		t.Run(format+"/"+tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			data := fmt.Sprintf("version: '3'\nfileserver:\n  address: %q\n  serve: [{prefix: /}]\n", tc.address)
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
			if cfg.Has("fileserver.unix_socket") != (tc.options != "") {
				t.Fatal("provider did not preserve block presence")
			}
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
