package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const mutualAuthProtocol = "forge-mutual-p256-v1"

type serverIdentity struct {
	AgentID     string
	PublicKey   pushJWK
	Fingerprint string
	key         *ecdsa.PrivateKey
}

type authorizedDevice struct {
	Name        string  `json:"name"`
	DeviceID    string  `json:"deviceId"`
	PublicKey   pushJWK `json:"publicKey"`
	Fingerprint string  `json:"fingerprint"`
	key         *ecdsa.PublicKey
}

type authorizedDeviceFile struct {
	Version int                `json:"version"`
	Devices []authorizedDevice `json:"devices"`
}

type clientAuthPolicy struct {
	identity *serverIdentity
	devices  map[string]authorizedDevice
	now      func() time.Time
}

func loadServerIdentity() (*serverIdentity, error) {
	manager, err := newPushManager()
	if err != nil {
		return nil, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.loadOrCreateLocked(); err != nil {
		return nil, err
	}
	fingerprint := fingerprintJWK(manager.identity.PublicKey)
	return &serverIdentity{
		AgentID: manager.identity.AgentID, PublicKey: manager.identity.PublicKey,
		Fingerprint: fingerprint, key: manager.key,
	}, nil
}

func loadClientAuthPolicy(path string) (*clientAuthPolicy, error) {
	identity, err := loadServerIdentity()
	if err != nil {
		return nil, fmt.Errorf("load server identity: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat authorized devices: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("authorized devices path must be a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("authorized devices file must not be group/world writable")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authorized devices: %w", err)
	}
	if len(encoded) > 4<<20 {
		return nil, errors.New("authorized devices file is too large")
	}
	var config authorizedDeviceFile
	if err := json.Unmarshal(encoded, &config); err != nil {
		return nil, fmt.Errorf("decode authorized devices: %w", err)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("authorized devices version must be 1")
	}
	if len(config.Devices) > 1024 {
		return nil, errors.New("authorized device count exceeds 1024")
	}
	policy := &clientAuthPolicy{identity: identity, devices: make(map[string]authorizedDevice), now: time.Now}
	fingerprints := make(map[string]bool)
	for index := range config.Devices {
		device := config.Devices[index]
		if strings.TrimSpace(device.DeviceID) == "" {
			return nil, fmt.Errorf("authorized device %d has no deviceId", index)
		}
		key, err := publicKeyFromJWK(device.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("authorized device %q: %w", device.DeviceID, err)
		}
		fingerprint := fingerprintJWK(device.PublicKey)
		if device.Fingerprint != fingerprint {
			return nil, fmt.Errorf("authorized device %q fingerprint does not match public key", device.DeviceID)
		}
		if _, duplicate := policy.devices[device.DeviceID]; duplicate {
			return nil, fmt.Errorf("duplicate authorized deviceId %q", device.DeviceID)
		}
		if fingerprints[fingerprint] {
			return nil, fmt.Errorf("duplicate authorized device fingerprint %q", fingerprint)
		}
		fingerprints[fingerprint] = true
		device.key = key
		policy.devices[device.DeviceID] = device
	}
	return policy, nil
}

func publicKeyFromJWK(jwk pushJWK) (*ecdsa.PublicKey, error) {
	if jwk.KTY != "EC" || jwk.CRV != "P-256" {
		return nil, errors.New("public key must be EC P-256")
	}
	xBytes, err := decodeBase64URL(jwk.X)
	if err != nil || len(xBytes) != 32 {
		return nil, errors.New("public key x coordinate must be 32-byte base64url")
	}
	yBytes, err := decodeBase64URL(jwk.Y)
	if err != nil || len(yBytes) != 32 {
		return nil, errors.New("public key y coordinate must be 32-byte base64url")
	}
	key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xBytes), Y: new(big.Int).SetBytes(yBytes)}
	if !key.Curve.IsOnCurve(key.X, key.Y) {
		return nil, errors.New("public key is not on P-256")
	}
	return key, nil
}

func fingerprintJWK(jwk pushJWK) string {
	digest := sha256.Sum256([]byte("P-256." + jwk.X + "." + jwk.Y))
	return base64URL(digest[:])
}

func signP256(key *ecdsa.PrivateKey, payload string) (string, error) {
	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	return base64URL(append(fixedBytes(r.Bytes(), 32), fixedBytes(s.Bytes(), 32)...)), nil
}

func verifyP256(key *ecdsa.PublicKey, payload, signature string) bool {
	raw, err := decodeBase64URL(signature)
	if err != nil || len(raw) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(payload))
	return ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}

func serverAuthPayload(command backendCommand, identity *serverIdentity) string {
	return strings.Join([]string{
		"FORGE-SERVER-AUTH-V1", command.ConnectionID, command.DeviceID,
		command.DeviceFingerprint, command.ClientNonce, command.ServerNonce,
		identity.AgentID, identity.Fingerprint, fmt.Sprint(command.ExpiresAt),
		command.ClientEphemeralKey.X, command.ClientEphemeralKey.Y,
		command.ServerEphemeralKey.X, command.ServerEphemeralKey.Y, command.Cipher,
	}, "\n")
}

func deviceAuthPayload(command backendCommand, identity *serverIdentity) string {
	return strings.Join([]string{
		"FORGE-DEVICE-AUTH-V1", command.ConnectionID, command.DeviceID,
		command.DeviceFingerprint, command.ClientNonce, command.ServerNonce,
		identity.AgentID, identity.Fingerprint, fmt.Sprint(command.ExpiresAt),
		command.ClientEphemeralKey.X, command.ClientEphemeralKey.Y,
		command.ServerEphemeralKey.X, command.ServerEphemeralKey.Y, command.Cipher,
	}, "\n")
}

func (policy *clientAuthPolicy) authenticate(
	ctx context.Context,
	decode func(*backendCommand) error,
	reply func(backendResponse) error,
) (authorizedDevice, *secureSession, error) {
	if err := reply(backendResponse{Type: "auth_required", ProtocolVersion: mutualAuthProtocol}); err != nil {
		return authorizedDevice{}, nil, err
	}
	var probe backendCommand
	if err := decodeWithContext(ctx, decode, &probe); err != nil {
		return authorizedDevice{}, nil, err
	}
	device, known := policy.devices[probe.DeviceID]
	if probe.Type != "auth_probe" || probe.AuthProtocol != mutualAuthProtocol || !known ||
		probe.DeviceFingerprint != device.Fingerprint || !validAuthToken(probe.ClientNonce, 32) ||
		probe.Cipher != sessionCipher {
		return authorizedDevice{}, nil, errors.New("authentication failed")
	}
	connectionID, err := randomBase64URL(18)
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	serverNonce, err := randomBase64URL(32)
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	serverEphemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	serverEphemeralJWK, err := ephemeralJWK(serverEphemeral.PublicKey())
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	if _, err := ecdhPublicFromJWK(probe.ClientEphemeralKey); err != nil {
		return authorizedDevice{}, nil, errors.New("authentication failed")
	}
	expiresAt := policy.now().Add(30 * time.Second).Unix()
	challenge := backendCommand{
		ConnectionID: connectionID, DeviceID: device.DeviceID,
		DeviceFingerprint: device.Fingerprint, ClientNonce: probe.ClientNonce,
		ServerNonce: serverNonce, ExpiresAt: expiresAt,
		ClientEphemeralKey: probe.ClientEphemeralKey, ServerEphemeralKey: serverEphemeralJWK, Cipher: sessionCipher,
	}
	signature, err := signP256(policy.identity.key, serverAuthPayload(challenge, policy.identity))
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	if err := reply(backendResponse{
		ID: probe.ID, Type: "auth_challenge", ProtocolVersion: mutualAuthProtocol,
		ConnectionID: connectionID, DeviceID: device.DeviceID, DeviceFingerprint: device.Fingerprint,
		ClientNonce: probe.ClientNonce, ServerNonce: serverNonce, AgentID: policy.identity.AgentID,
		AgentPublicKey: &policy.identity.PublicKey, AgentFingerprint: policy.identity.Fingerprint,
		ExpiresAt: expiresAt, ServerSignature: signature,
		ServerEphemeralKey: &serverEphemeralJWK, Cipher: sessionCipher,
	}); err != nil {
		return authorizedDevice{}, nil, err
	}
	var response backendCommand
	if err := decodeWithContext(ctx, decode, &response); err != nil {
		return authorizedDevice{}, nil, err
	}
	response.ClientNonce = probe.ClientNonce
	response.ServerNonce = serverNonce
	response.ExpiresAt = expiresAt
	response.ClientEphemeralKey = probe.ClientEphemeralKey
	response.ServerEphemeralKey = serverEphemeralJWK
	response.Cipher = sessionCipher
	if response.Type != "auth_response" {
		return authorizedDevice{}, nil, errors.New("authentication failed: response type")
	}
	if response.AuthProtocol != mutualAuthProtocol {
		return authorizedDevice{}, nil, errors.New("authentication failed: protocol")
	}
	if response.ConnectionID != connectionID {
		return authorizedDevice{}, nil, errors.New("authentication failed: connection ID")
	}
	if response.DeviceID != device.DeviceID {
		return authorizedDevice{}, nil, errors.New("authentication failed: device ID")
	}
	if response.DeviceFingerprint != device.Fingerprint {
		return authorizedDevice{}, nil, errors.New("authentication failed: fingerprint")
	}
	if policy.now().Unix() > expiresAt {
		return authorizedDevice{}, nil, errors.New("authentication failed: expired")
	}
	if !verifyP256(device.key, deviceAuthPayload(response, policy.identity), response.Signature) {
		return authorizedDevice{}, nil, errors.New("authentication failed: signature")
	}
	secure, err := newServerSecureSession(response, serverEphemeral, policy.identity)
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	confirmation, err := secure.encrypt(backendResponse{Type: "key_confirmation", ConnectionID: connectionID, Success: true})
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	if err := reply(confirmation); err != nil {
		return authorizedDevice{}, nil, err
	}
	var encryptedConfirm backendCommand
	if err := decodeWithContext(ctx, decode, &encryptedConfirm); err != nil {
		return authorizedDevice{}, nil, err
	}
	var clientConfirm backendCommand
	if err := secure.decrypt(encryptedConfirm, &clientConfirm); err != nil || clientConfirm.Type != "key_confirmation" || clientConfirm.ConnectionID != connectionID {
		return authorizedDevice{}, nil, errors.New("key confirmation failed")
	}
	authSuccess, err := secure.encrypt(backendResponse{ID: response.ID, Type: "auth_success", ProtocolVersion: mutualAuthProtocol, ConnectionID: connectionID, AgentID: policy.identity.AgentID, DeviceID: device.DeviceID, Success: true})
	if err != nil {
		return authorizedDevice{}, nil, err
	}
	if err := reply(authSuccess); err != nil {
		return authorizedDevice{}, nil, err
	}
	return device, secure, nil
}

func decodeWithContext(ctx context.Context, decode func(*backendCommand) error, command *backendCommand) error {
	done := make(chan error, 1)
	go func() { done <- decode(command) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func validAuthToken(value string, exactBytes int) bool {
	decoded, err := decodeBase64URL(value)
	return err == nil && len(decoded) == exactBytes
}

func defaultAuthorizedDevicesPath() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	return filepath.Join(configDir, "authorized-devices.json"), nil
}
