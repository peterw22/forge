package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultPushRelayURL = "https://forge-push.tingouw.com"

type pushJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type pushContentKey struct {
	KeyID string `json:"keyId"`
	Key   string `json:"key"`
}

type pushIdentityFile struct {
	AgentID        string                    `json:"agentId"`
	PrivateD       string                    `json:"privateD"`
	PublicKey      pushJWK                   `json:"publicKey"`
	Devices        map[string]bool           `json:"devices,omitempty"`
	PushKeys       map[string]pushContentKey `json:"pushKeys,omitempty"`
	DevicePlatform map[string]string         `json:"devicePlatforms,omitempty"`
}

type pushAuthorization struct {
	DeviceID           string   `json:"deviceId"`
	DisplayName        string   `json:"displayName,omitempty"`
	Fingerprint        string   `json:"fingerprint"`
	Platform           string   `json:"platform"`
	Scopes             []string `json:"scopes"`
	CreatedAt          int64    `json:"createdAt"`
	UpdatedAt          int64    `json:"updatedAt"`
	PushEndpointActive bool     `json:"pushEndpointActive"`
}

type pushPairing struct {
	PairingID        string   `json:"pairingId"`
	VerificationCode string   `json:"verificationCode"`
	Scopes           []string `json:"scopes"`
	ExpiresAt        int64    `json:"expiresAt"`
}

type pushManager struct {
	mu       sync.Mutex
	path     string
	relayURL string
	client   *http.Client
	identity pushIdentityFile
	key      *ecdsa.PrivateKey
	ready    bool
	initOnce sync.Once
	initDone chan struct{}
	initErr  error
	active   map[string]bool
	waiting  map[string]bool
	now      func() time.Time
}

func newPushManager() (*pushManager, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory for push identity: %w", err)
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	relayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PI_GO_PUSH_RELAY_URL")), "/")
	if relayURL == "" {
		relayURL = defaultPushRelayURL
	}
	return &pushManager{
		path: filepath.Join(configDir, "push", "identity.json"), relayURL: relayURL,
		client: &http.Client{Timeout: 15 * time.Second}, initDone: make(chan struct{}), active: map[string]bool{}, waiting: map[string]bool{}, now: time.Now,
	}, nil
}

func (manager *pushManager) ensureReady(ctx context.Context) error {
	manager.initOnce.Do(func() {
		go func() {
			manager.mu.Lock()
			defer manager.mu.Unlock()
			defer close(manager.initDone)
			if err := manager.loadOrCreateLocked(); err != nil {
				manager.initErr = err
				return
			}
			if err := manager.registerLocked(context.Background()); err != nil {
				manager.initErr = err
				return
			}
			manager.ready = true
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-manager.initDone:
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return manager.initErr
	}
}

func (manager *pushManager) loadOrCreateLocked() error {
	encoded, err := os.ReadFile(manager.path)
	if err == nil {
		info, statErr := os.Stat(manager.path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return errors.New("push identity path must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return errors.New("push identity file permissions must be 0600 or stricter")
		}
		if len(encoded) > 1<<20 {
			return errors.New("push identity file is too large")
		}
		if err := json.Unmarshal(encoded, &manager.identity); err != nil {
			return fmt.Errorf("decode push identity: %w", err)
		}
		agentIDBytes, agentIDErr := decodeBase64URL(manager.identity.AgentID)
		if agentIDErr != nil || len(agentIDBytes) < 16 {
			return errors.New("agent ID must contain at least 128 random bits")
		}
		d, err := decodeBase64URL(manager.identity.PrivateD)
		if err != nil || len(d) != 32 {
			return errors.New("push private key must be a 32-byte P-256 scalar")
		}
		curve := elliptic.P256()
		scalar := new(big.Int).SetBytes(d)
		if scalar.Sign() <= 0 || scalar.Cmp(curve.Params().N) >= 0 {
			return errors.New("push private key scalar is outside P-256 range")
		}
		manager.key = &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve}, D: scalar}
		manager.key.PublicKey.X, manager.key.PublicKey.Y = curve.ScalarBaseMult(d)
		expected := pushJWK{KTY: "EC", CRV: "P-256", X: base64URL(fixedBytes(manager.key.X.Bytes(), 32)), Y: base64URL(fixedBytes(manager.key.Y.Bytes(), 32))}
		if manager.identity.PublicKey != expected {
			return errors.New("stored push public key does not match private key")
		}
		if manager.identity.Devices == nil {
			manager.identity.Devices = map[string]bool{}
		}
		if manager.identity.PushKeys == nil {
			manager.identity.PushKeys = map[string]pushContentKey{}
		}
		if manager.identity.DevicePlatform == nil {
			manager.identity.DevicePlatform = map[string]string{}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read push identity: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate push identity: %w", err)
	}
	agentID, err := randomBase64URL(18)
	if err != nil {
		return err
	}
	manager.key = key
	manager.identity = pushIdentityFile{
		AgentID:        agentID,
		PrivateD:       base64URL(fixedBytes(key.D.Bytes(), 32)),
		PublicKey:      pushJWK{KTY: "EC", CRV: "P-256", X: base64URL(fixedBytes(key.X.Bytes(), 32)), Y: base64URL(fixedBytes(key.Y.Bytes(), 32))},
		Devices:        map[string]bool{},
		PushKeys:       map[string]pushContentKey{},
		DevicePlatform: map[string]string{},
	}
	return manager.saveLocked()
}

func (manager *pushManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(manager.path), 0700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manager.identity, "", "  ")
	if err != nil {
		return err
	}
	temporary := manager.path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, manager.path)
}

func (manager *pushManager) registerLocked(ctx context.Context) error {
	hostname, _ := os.Hostname()
	challenge := struct {
		ChallengeID    string `json:"challengeId"`
		SigningPayload string `json:"signingPayload"`
	}{}
	if err := manager.publicJSON(ctx, http.MethodPost, "/v1/registration-challenges", map[string]any{
		"principalType": "agent", "principalId": manager.identity.AgentID,
		"publicKey": manager.identity.PublicKey, "displayName": hostname,
	}, &challenge); err != nil {
		return fmt.Errorf("request push registration challenge: %w", err)
	}
	signature, err := manager.sign(challenge.SigningPayload)
	if err != nil {
		return err
	}
	if err := manager.publicJSON(ctx, http.MethodPost, "/v1/registrations", map[string]any{
		"challengeId": challenge.ChallengeID, "signature": signature,
	}, nil); err != nil {
		return fmt.Errorf("register push identity: %w", err)
	}
	return nil
}

func (manager *pushManager) Identity(ctx context.Context) (string, pushJWK, string, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return "", pushJWK{}, "", err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	fingerprint := sha256.Sum256([]byte("P-256." + manager.identity.PublicKey.X + "." + manager.identity.PublicKey.Y))
	return manager.identity.AgentID, manager.identity.PublicKey, base64URL(fingerprint[:]), nil
}

func (manager *pushManager) ConnectionProof(challenge, deviceID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.ready {
		return "", errors.New("push identity is not ready")
	}
	return manager.sign("FORGE-AGENT-CONNECTION-V1\n" + challenge + "\n" + deviceID)
}

func (manager *pushManager) DeviceAuthorized(ctx context.Context, deviceID string) (bool, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return false, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var result struct {
		Authorizations []pushAuthorization `json:"authorizations"`
	}
	path := "/v1/agents/" + manager.identity.AgentID + "/authorizations"
	if err := manager.signedJSONLocked(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, err
	}
	remoteAuthorized := false
	for _, authorization := range result.Authorizations {
		if authorization.DeviceID == deviceID {
			remoteAuthorized = true
			break
		}
	}
	localAuthorized := manager.identity.Devices[deviceID]
	if localAuthorized && !remoteAuthorized {
		delete(manager.identity.Devices, deviceID)
		delete(manager.identity.PushKeys, deviceID)
		delete(manager.identity.DevicePlatform, deviceID)
		if err := manager.saveLocked(); err != nil {
			return false, err
		}
	}
	// Relay authorization and local content-key state are both required. If the
	// relay link survived but local state was lost, normal pairing provisions a
	// fresh key and repairs both sides.
	return remoteAuthorized && localAuthorized, nil
}

func (manager *pushManager) ListAuthorizations(ctx context.Context) ([]pushAuthorization, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return nil, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var result struct {
		Authorizations []pushAuthorization `json:"authorizations"`
	}
	path := "/v1/agents/" + manager.identity.AgentID + "/authorizations"
	if err := manager.signedJSONLocked(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Authorizations, nil
}

func (manager *pushManager) RevokeAuthorization(ctx context.Context, deviceID string) error {
	if err := manager.ensureReady(ctx); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	path := "/v1/agents/" + manager.identity.AgentID + "/authorizations/" + deviceID
	if err := manager.signedJSONLocked(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return err
	}
	delete(manager.identity.Devices, deviceID)
	delete(manager.identity.PushKeys, deviceID)
	delete(manager.identity.DevicePlatform, deviceID)
	return manager.saveLocked()
}

func (manager *pushManager) CreatePairing(ctx context.Context, deviceID string) (pushPairing, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return pushPairing{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var pairing pushPairing
	err := manager.signedJSONLocked(ctx, http.MethodPost, "/v1/agents/"+manager.identity.AgentID+"/pairings", map[string]any{
		"deviceId": deviceID, "scopes": []string{"notify.approval", "notify.completed"},
	}, &pairing)
	return pairing, err
}

func (manager *pushManager) CompletePairing(ctx context.Context, deviceID, pairingID string) (bool, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return false, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var result struct {
		Status string `json:"status"`
	}
	path := "/v1/agents/" + manager.identity.AgentID + "/pairings/" + pairingID
	if err := manager.signedJSONLocked(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, err
	}
	if result.Status != "approved" {
		return false, nil
	}
	manager.identity.Devices[deviceID] = true
	return true, manager.saveLocked()
}

func (manager *pushManager) SetDevicePlatform(deviceID, platform string) error {
	if platform != "ios" && platform != "android" && platform != "macos" {
		return errors.New("unsupported push device platform")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.identity.DevicePlatform[deviceID] == platform {
		return nil
	}
	manager.identity.DevicePlatform[deviceID] = platform
	return manager.saveLocked()
}

func (manager *pushManager) PushKey(ctx context.Context, deviceID string) (pushContentKey, string, error) {
	if err := manager.ensureReady(ctx); err != nil {
		return pushContentKey{}, "", err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.identity.Devices[deviceID] {
		return pushContentKey{}, "", errors.New("device is not authorized for push notifications")
	}
	key := manager.identity.PushKeys[deviceID]
	if key.KeyID == "" || key.Key == "" {
		keyID, err := randomBase64URL(18)
		if err != nil {
			return pushContentKey{}, "", err
		}
		rawKey := make([]byte, 32)
		if _, err := rand.Read(rawKey); err != nil {
			return pushContentKey{}, "", err
		}
		key = pushContentKey{KeyID: keyID, Key: base64URL(rawKey)}
		manager.identity.PushKeys[deviceID] = key
		if err := manager.saveLocked(); err != nil {
			return pushContentKey{}, "", err
		}
	}
	payload := strings.Join([]string{"FORGE-PUSH-KEY-V1", manager.identity.AgentID, deviceID, key.KeyID, key.Key}, "\n")
	signature, err := manager.sign(payload)
	return key, signature, err
}

func (manager *pushManager) ObserveStatus(session string, active, waiting bool) {
	manager.mu.Lock()
	manager.active[session] = active
	manager.waiting[session] = waiting
	manager.mu.Unlock()
}

func (manager *pushManager) NotifyCompletion(session, summary string) {
	manager.notifyDetailed(session, "session_completed", summary)
}

func (manager *pushManager) NotifyApproval(session, summary string) {
	manager.notifyDetailed(session, "approval_required", summary)
}

func (manager *pushManager) notifyDetailed(session, eventType, summary string) {
	manager.mu.Lock()
	ready := manager.ready
	devices := make([]string, 0, len(manager.identity.Devices))
	for device, enabled := range manager.identity.Devices {
		if enabled {
			devices = append(devices, device)
		}
	}
	manager.mu.Unlock()
	if !ready || len(devices) == 0 {
		return
	}
	if err := validatePushNotificationSummary(summary); err != nil {
		if eventType == "approval_required" {
			summary = "This operation requires safety approval."
		} else {
			summary = "The agent finished its response."
		}
	}
	for _, device := range devices {
		device := device
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			eventID, err := randomBase64URL(18)
			if err != nil {
				return
			}
			if err := manager.sendEvent(ctx, device, eventID, eventType, session, summary); err != nil {
				fmt.Fprintln(os.Stderr, "push notification:", err)
			}
		}()
	}
}

func validatePushNotificationSummary(summary string) error {
	return validateNotificationSummary(summary)
}

func (manager *pushManager) sendEvent(ctx context.Context, deviceID, eventID, eventType, session, notificationSummary string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	path := "/v1/agents/" + manager.identity.AgentID + "/events"
	key := manager.identity.PushKeys[deviceID]
	if key.KeyID == "" || key.Key == "" {
		return errors.New("push encryption key is not provisioned")
	}
	if eventID == "" {
		var err error
		eventID, err = randomBase64URL(18)
		if err != nil {
			return err
		}
	}
	envelope, err := manager.encryptPushEvent(deviceID, eventID, key, eventType, session, notificationSummary)
	if err != nil {
		return err
	}
	return manager.signedJSONLocked(ctx, http.MethodPost, path, map[string]any{
		"deviceId": deviceID, "eventId": eventID, "encrypted": envelope,
	}, nil)
}

type encryptedPushEnvelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func (manager *pushManager) encryptPushEvent(deviceID, eventID string, key pushContentKey, eventType, session, notificationSummary string) (encryptedPushEnvelope, error) {
	rawKey, err := decodeBase64URL(key.Key)
	if err != nil || len(rawKey) != 32 {
		return encryptedPushEnvelope{}, errors.New("invalid push content key")
	}
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return encryptedPushEnvelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedPushEnvelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return encryptedPushEnvelope{}, err
	}
	approval := eventType == "approval_required"
	if approval {
		if err := validatePushNotificationSummary(notificationSummary); err != nil {
			notificationSummary = "This operation requires safety approval."
		}
	} else if err := validatePushNotificationSummary(notificationSummary); err != nil {
		notificationSummary = "The agent finished its response."
	}
	plaintext, err := json.Marshal(map[string]any{
		"type": eventType, "sessionId": session,
		"title": map[bool]string{true: "Forge approval required", false: "Forge session completed"}[approval],
		"body":  notificationSummary,
	})
	if err != nil {
		return encryptedPushEnvelope{}, err
	}
	aad := strings.Join([]string{"FORGE-PUSH-CONTENT-V1", manager.identity.AgentID, deviceID, eventID, key.KeyID}, "\n")
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(aad))
	return encryptedPushEnvelope{Version: 1, KeyID: key.KeyID, Nonce: base64URL(nonce), Ciphertext: base64URL(sealed)}, nil
}

func (manager *pushManager) publicJSON(ctx context.Context, method, path string, request any, response any) error {
	var body []byte
	var err error
	if request != nil {
		body, err = json.Marshal(request)
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, manager.relayURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return manager.doJSON(req, response)
}

func (manager *pushManager) signedJSONLocked(ctx context.Context, method, path string, request any, response any) error {
	var body []byte
	var err error
	if request != nil {
		body, err = json.Marshal(request)
	}
	if err != nil {
		return err
	}
	timestamp := fmt.Sprint(manager.now().Unix())
	nonce, err := randomBase64URL(18)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	payload := strings.Join([]string{"FORGE-REQUEST-V1", method, path, timestamp, nonce, base64URL(hash[:])}, "\n")
	signature, err := manager.sign(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, manager.relayURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forge-Principal-Type", "agent")
	req.Header.Set("X-Forge-Principal-ID", manager.identity.AgentID)
	req.Header.Set("X-Forge-Timestamp", timestamp)
	req.Header.Set("X-Forge-Nonce", nonce)
	req.Header.Set("X-Forge-Signature", signature)
	return manager.doJSON(req, response)
}

func (manager *pushManager) doJSON(request *http.Request, response any) error {
	result, err := manager.client.Do(request)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, 1<<20))
	if err != nil {
		return err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.Error.Message == "" {
			failure.Error.Message = result.Status
		}
		return errors.New(failure.Error.Message)
	}
	if response != nil && len(body) > 0 {
		return json.Unmarshal(body, response)
	}
	return nil
}

func (manager *pushManager) sign(payload string) (string, error) {
	hash := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, manager.key, hash[:])
	if err != nil {
		return "", err
	}
	raw := append(fixedBytes(r.Bytes(), 32), fixedBytes(s.Bytes(), 32)...)
	return base64URL(raw), nil
}

func fixedBytes(value []byte, size int) []byte {
	result := make([]byte, size)
	copy(result[size-len(value):], value)
	return result
}
func base64URL(value []byte) string                { return base64.RawURLEncoding.EncodeToString(value) }
func decodeBase64URL(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }
func randomBase64URL(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64URL(value), nil
}
