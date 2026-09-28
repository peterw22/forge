//go:build !linux && !darwin

package main

import (
	"errors"
	"net"
)

func verifyUnixPeer(connection *net.UnixConn) error {
	return errors.New("same-user Unix IPC is supported only on Linux and macOS")
}
