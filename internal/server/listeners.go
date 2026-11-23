package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/systemd"
)

// listenAPI uses the systemd Unix stream listener named api or binds a private Unix socket.
// The caller must hold the instance lock. created reports a self-bound socket.
func listenAPI(ctx context.Context, socket string) (ln net.Listener, created bool, err error) {
	listeners, err := systemd.Listeners()
	if err != nil {
		return nil, false, fmt.Errorf("systemd listeners: %w", err)
	}
	for name, listener := range listeners {
		if name != "api" {
			_ = listener.Close()
		}
	}
	if ln := listeners["api"]; ln != nil {
		// AIDEV-NOTE: an activation name does not constrain the transport.
		// Require a Unix stream socket to keep the API off TCP and packet sockets.
		api, ok := ln.(*net.UnixListener)
		if !ok || api.Addr().Network() != "unix" {
			_ = ln.Close()
			return nil, false, errors.New("systemd API listener must be a Unix stream socket")
		}
		return ln, false, nil
	}
	if err := config.CheckSocketPath(socket); err != nil {
		return nil, false, err
	}
	if err := fsutil.EnsureDir0700(filepath.Dir(socket)); err != nil {
		return nil, false, fmt.Errorf("prepare socket directory: %w", err)
	}
	// AIDEV-NOTE: the instance lock makes a leftover socket safe to unlink.
	// Check directory ownership and tighten permissions first; never remove a
	// regular file, directory or symlink accidentally configured as the socket.
	info, err := os.Lstat(socket)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("stat API socket: %w", err)
	}
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, false, fmt.Errorf("API socket %q: existing path is not a socket", socket)
		}
		if err := probeStaleSocket(ctx, socket); err != nil {
			return nil, false, err
		}
		if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("remove stale API socket: %w", err)
		}
	}
	ln, err = net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, false, fmt.Errorf("listen on API socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, false, fmt.Errorf("chmod API socket: %w", err)
	}
	return ln, true, nil
}

// probeStaleSocket returns nil only for a stale socket file: one whose connect
// is refused (or that vanished meanwhile).
//
// AIDEV-NOTE: final review A-Minor-4. The instance lock proves no other
// taskronin server runs, but NOT that nothing listens on the path: with
// taskronin.socket active and the service stopped, systemd owns it. Unlinking
// it would kill socket activation until `systemctl --user restart
// taskronin.socket`. A connection that succeeds, or any failure other than
// ECONNREFUSED/ENOENT, means "in use or unknown": refuse and keep the file.
// Unix connects never wait on the network; the timeout only bounds the call.
func probeStaleSocket(ctx context.Context, socket string) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("API socket %s is in use — is taskronin.socket active? use `systemctl --user start taskronin.service`", socket)
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("API socket %s may be in use (%w); refusing to remove it — is taskronin.socket active? use `systemctl --user start taskronin.service`", socket, err)
}
