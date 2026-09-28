//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnixPeerModeValidation(t *testing.T) {
	for _, endpoint := range []string{"", "tcp://127.0.0.1:7346", "ws://127.0.0.1:7346/ws", "unix://relative.sock"} {
		if validateUnixPeerMode(endpoint, "", true) == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	if validateUnixPeerMode("unix:///tmp/test.sock", "", true) != nil {
		t.Fatal("rejected absolute Unix endpoint")
	}
	if validateUnixPeerMode("unix:///tmp/test.sock", "/whitelist", true) == nil {
		t.Fatal("accepted conflicting authentication modes")
	}
	if requirePeerUID(42, 42) != nil || requirePeerUID(41, 42) == nil {
		t.Fatal("incorrect peer UID policy")
	}
	if err := serveSocket(nil, "tcp://127.0.0.1:0", false, nil); err == nil {
		t.Fatal("TCP accepted no authentication")
	}
	if err := serveSocketWithPeerAuth(nil, "tcp://127.0.0.1:0", false, nil, true); err == nil {
		t.Fatal("TCP accepted Unix authentication")
	}
}

func TestUnixBridgeVerifiesPeerAndDisconnectsWithoutShutdown(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "forge-unix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	path := filepath.Join(directory, "a.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	input, writer := io.Pipe()
	defer writer.Close()
	defer input.Close()
	var output bytes.Buffer
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridgeUnix(path, input, &output) }()
	peer, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := verifyUnixPeer(peer); err != nil {
		t.Fatal(err)
	}
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	go func() {
		io.WriteString(writer, "{\"type\":\"get_state\"}\n")
		writer.Close()
	}()
	data, err := io.ReadAll(peer)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"type\":\"get_state\"}\n" || strings.Contains(string(data), "shutdown") {
		t.Fatalf("unexpected forwarded data: %q", data)
	}
	select {
	case err := <-bridgeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit on stdin EOF")
	}
	// The independently managed socket remains available after disconnection.
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
}
