package systemd

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Notify sends one sd_notify datagram. An unset NOTIFY_SOCKET is a no-op.
func Notify(state string) error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "@") {
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("connect to systemd notify socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}
	return nil
}
