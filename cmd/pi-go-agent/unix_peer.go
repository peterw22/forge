//go:build linux || darwin

package main

import (
	"fmt"
	"net"
	"os"
)

// Kernel credentials, not the socket's name or filesystem owner, establish
// trust. This deliberately trusts every process running as the same user.
func verifyUnixPeer(connection *net.UnixConn) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		uid, credentialErr = unixPeerUID(int(fd))
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return fmt.Errorf("read Unix peer credentials: %w", credentialErr)
	}
	return requirePeerUID(uid, uint32(os.Geteuid()))
}

func requirePeerUID(peer, current uint32) error {
	if peer != current {
		return fmt.Errorf("Unix peer UID %d does not match current UID %d", peer, current)
	}
	return nil
}
