package fileserver

import (
	"strings"
	"testing"

	"github.com/roadrunner-server/tcplisten"
)

func TestConfigUnixSocketInvalid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		options *tcplisten.UnixSocketOptions
		wantErr string
	}{
		{name: "empty address", options: &tcplisten.UnixSocketOptions{}, wantErr: "empty address"},
		{name: "TCP options", address: "127.0.0.1:0", options: &tcplisten.UnixSocketOptions{}, wantErr: "fileserver.unix_socket"},
		{name: "TCP scheme", address: "tcp://127.0.0.1:0", options: &tcplisten.UnixSocketOptions{Mode: "0600"}, wantErr: "fileserver.unix_socket"},
		{name: "empty UNIX path", address: "unix://", options: &tcplisten.UnixSocketOptions{}, wantErr: "fileserver.unix_socket"},
		{name: "invalid mode", address: "unix://test.sock", options: &tcplisten.UnixSocketOptions{Mode: "600"}, wantErr: "fileserver.unix_socket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Address:       tc.address,
				UnixSocket:    tc.options,
				Configuration: []*Cfg{{Prefix: "/"}},
			}
			err := cfg.Valid()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}
