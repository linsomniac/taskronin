// Package server serves Taskronin's HTTP API over a private Unix socket.
package server

import (
	"context"
	"net"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// Transport identifies the underlying connection type.
type Transport uint8

// Supported connection transports; zero denotes an unknown transport.
const (
	TransportUnix Transport = iota + 1
	TransportTCP
)

// ConnInfo records transport metadata captured when the server accepts a connection.
type ConnInfo struct {
	Transport Transport
	PeerUID   uint32
	PeerPID   int32
	PeerOK    bool           // SO_PEERCRED read succeeded.
	Remote    netip.AddrPort // TCP only.
}

type connInfoKey struct{}

// ConnContext is the http.Server connection hook that captures peer credentials.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	var ci ConnInfo
	switch c := c.(type) {
	case *net.UnixConn:
		ci.Transport = TransportUnix
		raw, err := c.SyscallConn()
		if err != nil {
			break
		}
		// AIDEV-NOTE: read SO_PEERCRED only while Control holds the descriptor
		// alive. Neither a Unix transport nor a failed read establishes trust;
		// PeerOK stays false unless both Control and getsockopt succeed.
		var cred *unix.Ucred
		var credErr error
		err = raw.Control(func(fd uintptr) {
			cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		})
		if err == nil && credErr == nil && cred != nil {
			ci.PeerUID = cred.Uid
			ci.PeerPID = cred.Pid
			ci.PeerOK = true
		}
	case *net.TCPConn:
		ci.Transport = TransportTCP
		if addr, ok := c.RemoteAddr().(*net.TCPAddr); ok {
			ci.Remote = addr.AddrPort()
		}
	}
	return withConnInfo(ctx, ci)
}

// ConnInfoFrom returns connection metadata, or its zero value if none was stored.
func ConnInfoFrom(ctx context.Context) ConnInfo {
	ci, _ := ctx.Value(connInfoKey{}).(ConnInfo)
	return ci
}

func withConnInfo(ctx context.Context, ci ConnInfo) context.Context {
	return context.WithValue(ctx, connInfoKey{}, ci)
}

func ourUID() uint32 {
	return uint32(os.Getuid()) //nolint:gosec // G115: Linux UIDs fit in uint32
}
