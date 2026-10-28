package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnContextUnix(t *testing.T) {
	ci := requestConnInfo(t, "unix", filepath.Join(t.TempDir(), "sock"))
	if ci.Transport != TransportUnix || !ci.PeerOK || ci.PeerUID != uint32(os.Getuid()) || ci.PeerPID != int32(os.Getpid()) {
		t.Fatalf("ConnInfo = %+v; want Unix peer UID %d, PID %d", ci, os.Getuid(), os.Getpid())
	}
	if ci.Remote.IsValid() {
		t.Fatalf("Unix Remote = %v; want invalid", ci.Remote)
	}
	if got := ourUID(); got != uint32(os.Getuid()) {
		t.Fatalf("ourUID = %d; want %d", got, os.Getuid())
	}
}

func TestConnContextTCP(t *testing.T) {
	ci := requestConnInfo(t, "tcp", "127.0.0.1:0")
	if ci.Transport != TransportTCP || !ci.Remote.Addr().IsLoopback() || ci.Remote.Port() == 0 {
		t.Fatalf("ConnInfo = %+v; want TCP with a loopback remote address and port", ci)
	}
	if ci.PeerOK || ci.PeerUID != 0 || ci.PeerPID != 0 {
		t.Fatalf("TCP has Unix credentials: %+v", ci)
	}
}

func TestConnContextFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	dialer := net.Dialer{Timeout: 5 * time.Second}
	closed, err := dialer.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	pipe, other := net.Pipe()
	t.Cleanup(func() { _ = pipe.Close(); _ = other.Close() })
	for _, tc := range []struct {
		name string
		conn net.Conn
		want Transport
	}{
		{"closed_unix", closed, TransportUnix},
		{"uninitialized_unix", new(net.UnixConn), TransportUnix},
		{"unsupported", pipe, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// AIDEV-NOTE: failed or unsupported connections must overwrite any
			// inherited credentials instead of retaining a trusted parent value.
			ctx := withConnInfo(t.Context(), ConnInfo{Transport: TransportUnix, PeerOK: true, PeerUID: ourUID(), PeerPID: int32(os.Getpid())})
			if got := ConnInfoFrom(ConnContext(ctx, tc.conn)); got != (ConnInfo{Transport: tc.want}) {
				t.Fatalf("ConnInfo = %+v; want transport %d with no credentials", got, tc.want)
			}
		})
	}
}

func TestConnInfoFrom(t *testing.T) {
	if got := ConnInfoFrom(t.Context()); got != (ConnInfo{}) {
		t.Fatalf("missing ConnInfo = %+v; want zero value", got)
	}
	want := ConnInfo{Transport: TransportTCP, Remote: netip.MustParseAddrPort("127.0.0.1:1234")}
	ctx, cancel := context.WithCancel(t.Context())
	gotCtx := withConnInfo(ctx, want)
	cancel()
	if got := ConnInfoFrom(gotCtx); got != want {
		t.Fatalf("ConnInfo = %+v; want %+v", got, want)
	}
	if !errors.Is(gotCtx.Err(), context.Canceled) {
		t.Fatal("connection context did not preserve parent cancellation")
	}
}

func requestConnInfo(t *testing.T, network, address string) ConnInfo {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), network, address)
	if err != nil {
		t.Fatal(err)
	}
	infos := make(chan ConnInfo, 1)
	srv := &http.Server{
		ConnContext:       ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			infos <- ConnInfoFrom(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	})
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, ln.Addr().String())
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://taskronin/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP status = %d; want 204", resp.StatusCode)
	}
	select {
	case ci := <-infos:
		return ci
	case <-ctx.Done():
		t.Fatal("handler did not record connection info")
		return ConnInfo{}
	}
}
