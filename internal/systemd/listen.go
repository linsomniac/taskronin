// Package systemd implements socket activation, notifications and user units.
package systemd

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Listeners consumes systemd's named descriptors and clears the activation
// environment. A process that was not socket-activated gets nil, nil.
func Listeners() (map[string]net.Listener, error) {
	defer func() {
		_ = os.Unsetenv("LISTEN_PID")
		_ = os.Unsetenv("LISTEN_FDS")
		_ = os.Unsetenv("LISTEN_FDNAMES")
	}()
	return listenersFrom(os.Getenv, os.Getpid(), 3)
}

func listenersFrom(getenv func(string) string, pid, firstFD int) (map[string]net.Listener, error) {
	pidText := getenv("LISTEN_PID")
	if pidText == "" {
		return nil, nil
	}
	listenPID, err := strconv.Atoi(pidText)
	if err != nil {
		return nil, fmt.Errorf("parse LISTEN_PID: %w", err)
	}
	if listenPID != pid {
		return nil, nil
	}
	fdsText := getenv("LISTEN_FDS")
	if fdsText == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(fdsText)
	if err != nil {
		return nil, fmt.Errorf("parse LISTEN_FDS: %w", err)
	}
	if firstFD < 0 || n < 0 || n > math.MaxInt-firstFD {
		return nil, fmt.Errorf("invalid LISTEN_FDS descriptor range")
	}
	if n == 0 {
		return nil, nil
	}
	names := strings.Split(getenv("LISTEN_FDNAMES"), ":")
	if len(names) != n {
		return nil, fmt.Errorf("LISTEN_FDNAMES has %d names for %d descriptors", len(names), n)
	}
	seen := make(map[string]bool, n)
	for _, name := range names {
		if name == "" || seen[name] {
			return nil, fmt.Errorf("LISTEN_FDNAMES contains an empty or duplicate name %q", name)
		}
		seen[name] = true
	}

	// AIDEV-NOTE: FileListener duplicates fds. Own and close every inherited
	// descriptor, including those after a failed conversion, and close any
	// completed listeners on failure so partial activation cannot leak sockets.
	files := make([]*os.File, 0, n)
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	for i, name := range names {
		fd := firstFD + i
		unix.CloseOnExec(fd)
		f := os.NewFile(uintptr(fd), name)
		files = append(files, f)
	}
	listeners := make(map[string]net.Listener, n)
	for i, f := range files {
		ln, err := net.FileListener(f)
		if err != nil {
			for _, ln := range listeners {
				_ = ln.Close()
			}
			return nil, fmt.Errorf("listen on systemd descriptor %q: %w", names[i], err)
		}
		listeners[names[i]] = ln
	}
	return listeners, nil
}
