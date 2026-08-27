package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"testing"
)

func testSecureSessionPair(t *testing.T) (*secureSession, *secureSession) {
	t.Helper()
	serverIdentityKey, serverIdentityJWK, serverFingerprint := testAuthIdentity(t, "server")
	identity := &serverIdentity{AgentID: "agent", PublicKey: serverIdentityJWK, Fingerprint: serverFingerprint, key: serverIdentityKey}
	clientPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientJWK, _ := ephemeralJWK(clientPrivate.PublicKey())
	serverJWK, _ := ephemeralJWK(serverPrivate.PublicKey())
	command := backendCommand{ConnectionID: "connection", ClientNonce: "client-nonce", ServerNonce: "server-nonce", DeviceFingerprint: "device-fingerprint", ClientEphemeralKey: clientJWK, ServerEphemeralKey: serverJWK}
	secret, err := clientPrivate.ECDH(serverPrivate.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	client, err := deriveSecureSession(command, identity, secret, true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newServerSecureSession(command, serverPrivate, identity)
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func responseAsCommand(t *testing.T, response backendResponse) backendCommand {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var command backendCommand
	if err := json.Unmarshal(encoded, &command); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestSecureSessionRejectsTamperReplayAndWrongDirection(t *testing.T) {
	client, server := testSecureSessionPair(t)
	encrypted, err := client.encrypt(backendCommand{Type: "prompt", Message: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	command := responseAsCommand(t, encrypted)

	tampered := command
	raw, _ := decodeBase64URL(tampered.Ciphertext)
	raw[len(raw)-1] ^= 1
	tampered.Ciphertext = base64URL(raw)
	var destination backendCommand
	if err := server.decrypt(tampered, &destination); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}

	// Authentication failures do not consume sequence numbers, so the original
	// valid envelope must still decrypt exactly once.
	if err := server.decrypt(command, &destination); err != nil {
		t.Fatal(err)
	}
	if destination.Message != "secret" {
		t.Fatalf("message=%q", destination.Message)
	}
	if err := server.decrypt(command, &destination); err == nil {
		t.Fatal("replayed envelope accepted")
	}

	next, err := client.encrypt(backendCommand{Type: "prompt", Message: "next"})
	if err != nil {
		t.Fatal(err)
	}
	skipped := responseAsCommand(t, next)
	skipped.EncryptedSequence++
	if err := server.decrypt(skipped, &destination); err == nil {
		t.Fatal("skipped sequence accepted")
	}

	serverOutbound, err := server.encrypt(backendResponse{Type: "response", Success: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.decrypt(responseAsCommand(t, serverOutbound), &destination); err == nil {
		t.Fatal("wrong-direction envelope accepted")
	}
}
