package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testAuthIdentity(t *testing.T, id string) (*ecdsa.PrivateKey, pushJWK, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := pushJWK{KTY: "EC", CRV: "P-256", X: base64URL(fixedBytes(key.X.Bytes(), 32)), Y: base64URL(fixedBytes(key.Y.Bytes(), 32))}
	return key, jwk, fingerprintJWK(jwk)
}

func TestMutualAuthenticationChallengeAndProof(t *testing.T) {
	serverKey, serverJWK, serverFingerprint := testAuthIdentity(t, "server")
	deviceKey, deviceJWK, deviceFingerprint := testAuthIdentity(t, "device")
	policy := &clientAuthPolicy{
		identity: &serverIdentity{AgentID: "agent-id", PublicKey: serverJWK, Fingerprint: serverFingerprint, key: serverKey},
		devices:  map[string]authorizedDevice{"device-id": {DeviceID: "device-id", PublicKey: deviceJWK, Fingerprint: deviceFingerprint, key: &deviceKey.PublicKey}},
		now:      time.Now,
	}
	clientEphemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientEphemeralJWK, err := ephemeralJWK(clientEphemeral.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	clientNonce, _ := randomBase64URL(32)
	commands := []backendCommand{{Type: "auth_probe", AuthProtocol: mutualAuthProtocol, DeviceID: "device-id", DeviceFingerprint: deviceFingerprint, ClientNonce: clientNonce, ClientEphemeralKey: clientEphemeralJWK, Cipher: sessionCipher}}
	var replies []backendResponse
	var clientSecure *secureSession
	decode := func(command *backendCommand) error {
		if len(commands) == 0 {
			return context.Canceled
		}
		*command = commands[0]
		commands = commands[1:]
		return nil
	}
	reply := func(response backendResponse) error {
		replies = append(replies, response)
		if response.Type == "auth_challenge" {
			challenge := backendCommand{ConnectionID: response.ConnectionID, DeviceID: response.DeviceID, DeviceFingerprint: response.DeviceFingerprint, ClientNonce: response.ClientNonce, ServerNonce: response.ServerNonce, ExpiresAt: response.ExpiresAt, ClientEphemeralKey: clientEphemeralJWK, ServerEphemeralKey: *response.ServerEphemeralKey, Cipher: response.Cipher}
			if !verifyP256(&serverKey.PublicKey, serverAuthPayload(challenge, policy.identity), response.ServerSignature) {
				t.Fatal("server proof did not verify")
			}
			signature, err := signP256(deviceKey, deviceAuthPayload(challenge, policy.identity))
			if err != nil {
				t.Fatal(err)
			}
			serverPublic, err := ecdhPublicFromJWK(*response.ServerEphemeralKey)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := clientEphemeral.ECDH(serverPublic)
			if err != nil {
				t.Fatal(err)
			}
			clientSecure, err = deriveSecureSession(challenge, policy.identity, secret, true)
			if err != nil {
				t.Fatal(err)
			}
			commands = append(commands, backendCommand{Type: "auth_response", AuthProtocol: mutualAuthProtocol, ConnectionID: response.ConnectionID, DeviceID: "device-id", DeviceFingerprint: deviceFingerprint, Signature: signature})
		} else if response.Type == "encrypted" && clientSecure != nil {
			var confirmation backendResponse
			if err := clientSecure.decrypt(backendCommand{Type: response.Type, EncryptedVersion: response.EncryptedVersion, EncryptedSequence: response.EncryptedSequence, Ciphertext: response.Ciphertext}, &confirmation); err != nil {
				t.Fatal(err)
			}
			if confirmation.Type == "key_confirmation" {
				encrypted, err := clientSecure.encrypt(backendCommand{Type: "key_confirmation", ConnectionID: confirmation.ConnectionID})
				if err != nil {
					t.Fatal(err)
				}
				commands = append(commands, backendCommand{Type: encrypted.Type, EncryptedVersion: encrypted.EncryptedVersion, EncryptedSequence: encrypted.EncryptedSequence, Ciphertext: encrypted.Ciphertext})
			}
		}
		return nil
	}
	device, secure, err := policy.authenticate(context.Background(), decode, reply)
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceID != "device-id" || secure == nil || len(replies) != 4 {
		t.Fatalf("device=%#v replies=%#v", device, replies)
	}
}

func TestAuthorizedDeviceFileRejectsFingerprintMismatch(t *testing.T) {
	_, jwk, _ := testAuthIdentity(t, "device")
	root := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", root)
	// Seed the reusable server identity so loading the policy does not contact any remote service.
	manager, err := newPushManager()
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	if err = manager.loadOrCreateLocked(); err != nil {
		t.Fatal(err)
	}
	manager.mu.Unlock()
	config := authorizedDeviceFile{Version: 1, Devices: []authorizedDevice{{DeviceID: "device-id", PublicKey: jwk, Fingerprint: "wrong"}}}
	encoded, _ := json.Marshal(config)
	path := filepath.Join(root, "authorized-devices.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClientAuthPolicy(path); err == nil {
		t.Fatal("mismatched fingerprint accepted")
	}
}

func TestAuthorizedDeviceFileRejectsWritablePermissions(t *testing.T) {
	_, jwk, fingerprint := testAuthIdentity(t, "device")
	root := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", root)
	manager, err := newPushManager()
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	if err = manager.loadOrCreateLocked(); err != nil {
		t.Fatal(err)
	}
	manager.mu.Unlock()
	config := authorizedDeviceFile{Version: 1, Devices: []authorizedDevice{{DeviceID: "device-id", PublicKey: jwk, Fingerprint: fingerprint}}}
	encoded, _ := json.Marshal(config)
	path := filepath.Join(root, "authorized-devices.json")
	if err := os.WriteFile(path, encoded, 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClientAuthPolicy(path); err == nil {
		t.Fatal("world-writable whitelist accepted")
	}
}
