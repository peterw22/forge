package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

func validateUnixPeerMode(endpoint, whitelist string, enabled bool) error {
	if !enabled {
		return nil
	}
	if !strings.HasPrefix(endpoint, "unix://") || !filepath.IsAbs(strings.TrimPrefix(endpoint, "unix://")) {
		return errors.New("--unix-peer-auth requires --listen unix:///absolute/path")
	}
	if strings.TrimSpace(whitelist) != "" {
		return errors.New("--unix-peer-auth and --authorized-devices are mutually exclusive")
	}
	return nil
}

// The desktop app uses this small bridge because Dart Socket does not expose
// SO_PEERCRED/getpeereid. Never forward protocol bytes before verifying the
// server's kernel credentials. Closing stdin disconnects, NEVER shuts down the
// independently managed agent.
func bridgeUnix(path string, input io.Reader, output io.Writer) error {
	path = strings.TrimPrefix(path, "unix://")
	if !filepath.IsAbs(path) {
		return errors.New("Unix socket path must be absolute")
	}
	connection, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("not a Unix connection")
	}
	if err := verifyUnixPeer(unixConnection); err != nil {
		return fmt.Errorf("untrusted Unix server: %w", err)
	}
	done := make(chan error, 2)
	go func() { _, err := io.Copy(connection, input); done <- err }()
	go func() { _, err := io.Copy(output, connection); done <- err }()
	return <-done
}
