package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncryptedPushContentHidesAndAuthenticatesMetadata(t *testing.T) {
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	manager := &pushManager{identity: pushIdentityFile{AgentID: "agent-identifier-12345"}}
	key := pushContentKey{KeyID: "key-identifier-123456", Key: base64URL(rawKey)}
	envelope, err := manager.encryptPushEvent("device-identifier-123", "event-identifier-1234", key, "approval_required", "private-session-name", "This operation may delete workspace files.")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope)
	for _, secret := range []string{"approval_required", "private-session-name", "Forge needs your feedback", "This operation may delete workspace files."} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("encrypted envelope leaked %q", secret)
		}
	}
	block, _ := aes.NewCipher(rawKey)
	gcm, _ := cipher.NewGCM(block)
	nonce, _ := decodeBase64URL(envelope.Nonce)
	ciphertext, _ := decodeBase64URL(envelope.Ciphertext)
	aad := strings.Join([]string{"FORGE-PUSH-CONTENT-V1", manager.identity.AgentID, "device-identifier-123", "event-identifier-1234", key.KeyID}, "\n")
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plaintext), "private-session-name") {
		t.Fatal("decrypted session is missing")
	}
	if !strings.Contains(string(plaintext), "This operation may delete workspace files.") ||
		!strings.Contains(string(plaintext), "Forge approval required") {
		t.Fatal("decrypted notification summary is missing")
	}
	ciphertext[0] ^= 1
	if _, err := gcm.Open(nil, nonce, ciphertext, []byte(aad)); err == nil {
		t.Fatal("tampered ciphertext authenticated")
	}
	ciphertext[0] ^= 1
	if _, err := gcm.Open(nil, nonce, ciphertext, []byte(aad+"!")); err == nil {
		t.Fatal("altered metadata authenticated")
	}
}
