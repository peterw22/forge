package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestDeviceAuthorizationIsReconciledWithWorkerOnEveryHandshake(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "device-identifier-12345"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/v1/agents/agent-identifier-12345/authorizations" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Forge-Principal-Type") != "agent" ||
			request.Header.Get("X-Forge-Principal-ID") != "agent-identifier-12345" ||
			request.Header.Get("X-Forge-Signature") == "" {
			t.Error("signed agent authentication headers are missing")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"authorizations":[]}`))
	}))
	defer server.Close()

	manager := readyPushManagerForTest(t, key, server, deviceID)
	authorized, err := manager.DeviceAuthorized(context.Background(), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		t.Fatal("stale local authorization was trusted after Worker revocation")
	}
	if requests != 1 {
		t.Fatalf("Worker authorization requests = %d, want 1", requests)
	}
	if manager.identity.Devices[deviceID] || manager.identity.PushKeys[deviceID].KeyID != "" || manager.identity.DevicePlatform[deviceID] != "" {
		t.Fatalf("stale local state remains: %#v", manager.identity)
	}
}

func TestDeviceAuthorizationRequiresBothWorkerAndLocalKeyState(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "device-identifier-12345"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"authorizations":[{"deviceId":"` + deviceID + `"}]}`))
	}))
	defer server.Close()

	manager := readyPushManagerForTest(t, key, server, deviceID)
	authorized, err := manager.DeviceAuthorized(context.Background(), deviceID)
	if err != nil || !authorized {
		t.Fatalf("matching Worker and local authorization = %v, %v", authorized, err)
	}
	delete(manager.identity.Devices, deviceID)
	authorized, err = manager.DeviceAuthorized(context.Background(), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		t.Fatal("Worker link without local content-key authorization was accepted")
	}
}

func TestRevokePushAuthorizationDeletesRelayAndLocalKeyState(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "device-identifier-12345"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			t.Errorf("method = %q", request.Method)
		}
		if request.URL.Path != "/v1/agents/agent-identifier-12345/authorizations/"+deviceID {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Forge-Principal-Type") != "agent" ||
			request.Header.Get("X-Forge-Principal-ID") != "agent-identifier-12345" ||
			request.Header.Get("X-Forge-Signature") == "" {
			t.Error("signed agent authentication headers are missing")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"revoked":true}`))
	}))
	defer server.Close()

	manager := readyPushManagerForTest(t, key, server, deviceID)

	if err := manager.RevokeAuthorization(context.Background(), deviceID); err != nil {
		t.Fatal(err)
	}
	if manager.identity.Devices[deviceID] || manager.identity.PushKeys[deviceID].KeyID != "" || manager.identity.DevicePlatform[deviceID] != "" {
		t.Fatalf("revoked local state remains: %#v", manager.identity)
	}
	encoded, err := json.Marshal(manager.identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{deviceID, "key-identifier-12345", "secret"} {
		if string(encoded) != "" && containsJSONText(encoded, secret) {
			t.Fatalf("persisted identity still contains %q: %s", secret, encoded)
		}
	}
}

func readyPushManagerForTest(t *testing.T, key *ecdsa.PrivateKey, server *httptest.Server, deviceID string) *pushManager {
	t.Helper()
	manager := &pushManager{
		path:     filepath.Join(t.TempDir(), "push", "identity.json"),
		relayURL: server.URL,
		client:   server.Client(),
		identity: pushIdentityFile{
			AgentID:        "agent-identifier-12345",
			Devices:        map[string]bool{deviceID: true},
			PushKeys:       map[string]pushContentKey{deviceID: {KeyID: "key-identifier-12345", Key: "secret"}},
			DevicePlatform: map[string]string{deviceID: "ios"},
		},
		key:      key,
		ready:    true,
		initDone: make(chan struct{}),
		now:      time.Now,
	}
	manager.initOnce.Do(func() {})
	close(manager.initDone)
	return manager
}

func containsJSONText(encoded []byte, value string) bool {
	for index := 0; index+len(value) <= len(encoded); index++ {
		if string(encoded[index:index+len(value)]) == value {
			return true
		}
	}
	return false
}
