package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	openAICodexClientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexDeviceVerificationURI = "https://auth.openai.com/codex/device"
	openAICodexDeviceRedirectURI     = "https://auth.openai.com/deviceauth/callback"
	openAICodexDeviceTimeout         = 15 * time.Minute
	openAICodexMinimumTokenValidity  = 5 * time.Minute
)

type codexAuthEndpoints struct {
	userCode string
	device   string
	token    string
	verify   string
}

var defaultCodexAuthEndpoints = codexAuthEndpoints{
	userCode: "https://auth.openai.com/api/accounts/deviceauth/usercode",
	device:   "https://auth.openai.com/api/accounts/deviceauth/token",
	token:    "https://auth.openai.com/oauth/token",
	verify:   openAICodexDeviceVerificationURI,
}

type codexCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

type codexAuthStatus struct {
	Authenticated bool
	AccountID     string
	ExpiresAt     int64
}

type codexDeviceCode struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       int64
}

type codexAuthManager struct {
	path        string
	client      *http.Client
	endpoints   codexAuthEndpoints
	now         func() time.Time
	mu          sync.Mutex
	loginMu     sync.Mutex
	loginActive bool
	loginCancel context.CancelFunc
}

func newCodexAuthManager() (*codexAuthManager, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory for Codex credentials: %w", err)
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	return newCodexAuthManagerAt(filepath.Join(configDir, "auth.json"), defaultCodexAuthEndpoints, http.DefaultClient), nil
}

func newCodexAuthManagerAt(path string, endpoints codexAuthEndpoints, client *http.Client) *codexAuthManager {
	return &codexAuthManager{path: path, endpoints: endpoints, client: client, now: time.Now}
}

func (auth *codexAuthManager) Token(_ string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("PI_GO_CODEX_TOKEN")); token != "" {
		return token, nil
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	credential, err := auth.loadLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("OpenAI sign-in required; use Forge Account → Sign in")
		}
		return "", err
	}
	if credential.AccessToken != "" && time.UnixMilli(credential.ExpiresAt).After(auth.now().Add(openAICodexMinimumTokenValidity)) {
		return credential.AccessToken, nil
	}
	if credential.RefreshToken == "" {
		return "", errors.New("OpenAI session cannot be refreshed; sign in again")
	}
	refreshed, err := auth.refreshLocked(context.Background(), credential.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := auth.saveLocked(refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (auth *codexAuthManager) Status() (codexAuthStatus, error) {
	if token := strings.TrimSpace(os.Getenv("PI_GO_CODEX_TOKEN")); token != "" {
		accountID, _ := codexAccountIDFromJWT(token)
		return codexAuthStatus{Authenticated: true, AccountID: accountID}, nil
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	credential, err := auth.loadLocked()
	if errors.Is(err, os.ErrNotExist) {
		return codexAuthStatus{}, nil
	}
	if err != nil {
		return codexAuthStatus{}, err
	}
	// A refresh token means the user remains signed in across access-token
	// expiry. Refresh is deferred until the provider actually needs a token.
	return codexAuthStatus{
		Authenticated: credential.RefreshToken != "" || time.UnixMilli(credential.ExpiresAt).After(auth.now()),
		AccountID:     credential.AccountID,
		ExpiresAt:     credential.ExpiresAt,
	}, nil
}

func (auth *codexAuthManager) BeginLogin() (context.Context, error) {
	auth.loginMu.Lock()
	defer auth.loginMu.Unlock()
	if auth.loginActive {
		return nil, errors.New("OpenAI sign-in is already in progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	auth.loginActive = true
	auth.loginCancel = cancel
	return ctx, nil
}

func (auth *codexAuthManager) EndLogin() {
	auth.loginMu.Lock()
	auth.loginActive = false
	auth.loginCancel = nil
	auth.loginMu.Unlock()
}

func (auth *codexAuthManager) CancelLogin() bool {
	auth.loginMu.Lock()
	cancel, active := auth.loginCancel, auth.loginActive
	auth.loginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return active
}

func (auth *codexAuthManager) LoginDeviceCode(ctx context.Context, notify func(codexDeviceCode)) (codexAuthStatus, error) {
	device, err := auth.requestDeviceCode(ctx)
	if err != nil {
		return codexAuthStatus{}, err
	}
	deadline := auth.now().Add(openAICodexDeviceTimeout)
	notify(codexDeviceCode{UserCode: device.UserCode, VerificationURI: auth.endpoints.verify, ExpiresAt: deadline.UnixMilli()})

	interval := time.Duration(device.IntervalSeconds * float64(time.Second))
	if interval < time.Second {
		interval = time.Second
	}
	for auth.now().Before(deadline) {
		code, pending, slowDown, pollErr := auth.pollDeviceCode(ctx, device)
		if pollErr != nil {
			return codexAuthStatus{}, pollErr
		}
		if !pending {
			credential, exchangeErr := auth.exchangeCode(ctx, code.AuthorizationCode, code.CodeVerifier)
			if exchangeErr != nil {
				return codexAuthStatus{}, exchangeErr
			}
			auth.mu.Lock()
			saveErr := auth.saveLocked(credential)
			auth.mu.Unlock()
			if saveErr != nil {
				return codexAuthStatus{}, saveErr
			}
			return codexAuthStatus{Authenticated: true, AccountID: credential.AccountID, ExpiresAt: credential.ExpiresAt}, nil
		}
		if slowDown {
			interval += 5 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return codexAuthStatus{}, errors.New("OpenAI sign-in cancelled")
		case <-timer.C:
		}
	}
	return codexAuthStatus{}, errors.New("OpenAI device sign-in timed out")
}

func (auth *codexAuthManager) Logout() error {
	if strings.TrimSpace(os.Getenv("PI_GO_CODEX_TOKEN")) != "" {
		return errors.New("cannot sign out while PI_GO_CODEX_TOKEN is set")
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	document, err := auth.loadAuthFileLocked()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(document.Providers, codexProviderID)
	return auth.saveAuthFileLocked(document)
}

type deviceAuthInfo struct {
	DeviceAuthID    string  `json:"device_auth_id"`
	UserCode        string  `json:"user_code"`
	IntervalSeconds float64 `json:"-"`
	Interval        any     `json:"interval"`
}

func (auth *codexAuthManager) requestDeviceCode(ctx context.Context) (deviceAuthInfo, error) {
	body, _ := json.Marshal(map[string]string{"client_id": openAICodexClientID})
	response, err := auth.do(ctx, http.MethodPost, auth.endpoints.userCode, "application/json", body)
	if err != nil {
		return deviceAuthInfo{}, fmt.Errorf("start OpenAI device sign-in: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return deviceAuthInfo{}, responseError("OpenAI device code request", response)
	}
	var raw struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return deviceAuthInfo{}, fmt.Errorf("decode OpenAI device code: %w", err)
	}
	interval := 5.0
	if len(raw.Interval) > 0 {
		if err := json.Unmarshal(raw.Interval, &interval); err != nil {
			var text string
			if json.Unmarshal(raw.Interval, &text) == nil {
				if _, scanErr := fmt.Sscan(strings.TrimSpace(text), &interval); scanErr != nil {
					return deviceAuthInfo{}, errors.New("invalid OpenAI device polling interval")
				}
			}
		}
	}
	if raw.DeviceAuthID == "" || raw.UserCode == "" || interval < 0 {
		return deviceAuthInfo{}, errors.New("OpenAI device code response is missing required fields")
	}
	return deviceAuthInfo{DeviceAuthID: raw.DeviceAuthID, UserCode: raw.UserCode, IntervalSeconds: interval}, nil
}

type deviceTokenCode struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

func (auth *codexAuthManager) pollDeviceCode(ctx context.Context, device deviceAuthInfo) (deviceTokenCode, bool, bool, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": device.DeviceAuthID, "user_code": device.UserCode})
	response, err := auth.do(ctx, http.MethodPost, auth.endpoints.device, "application/json", body)
	if err != nil {
		return deviceTokenCode{}, false, false, fmt.Errorf("poll OpenAI device sign-in: %w", err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 == 2 {
		var code deviceTokenCode
		if json.Unmarshal(payload, &code) != nil || code.AuthorizationCode == "" || code.CodeVerifier == "" {
			return deviceTokenCode{}, false, false, errors.New("OpenAI device authorization response is missing required fields")
		}
		return code, false, false, nil
	}
	var failure struct {
		Error any `json:"error"`
	}
	_ = json.Unmarshal(payload, &failure)
	errorCode := ""
	switch value := failure.Error.(type) {
	case string:
		errorCode = value
	case map[string]any:
		errorCode, _ = value["code"].(string)
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound || errorCode == "deviceauth_authorization_pending" {
		return deviceTokenCode{}, true, false, nil
	}
	if errorCode == "slow_down" {
		return deviceTokenCode{}, true, true, nil
	}
	return deviceTokenCode{}, false, false, fmt.Errorf("OpenAI device authorization failed (%d): %s", response.StatusCode, strings.TrimSpace(string(payload)))
}

func (auth *codexAuthManager) exchangeCode(ctx context.Context, code, verifier string) (codexCredential, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {openAICodexClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {openAICodexDeviceRedirectURI},
	}
	return auth.tokenRequest(ctx, values, "exchange")
}

func (auth *codexAuthManager) refreshLocked(ctx context.Context, refreshToken string) (codexCredential, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {openAICodexClientID},
	}
	return auth.tokenRequest(ctx, values, "refresh")
}

func (auth *codexAuthManager) tokenRequest(ctx context.Context, values url.Values, operation string) (codexCredential, error) {
	response, err := auth.do(ctx, http.MethodPost, auth.endpoints.token, "application/x-www-form-urlencoded", []byte(values.Encode()))
	if err != nil {
		return codexCredential{}, fmt.Errorf("OpenAI token %s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return codexCredential{}, responseError("OpenAI token "+operation, response)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return codexCredential{}, fmt.Errorf("decode OpenAI token %s: %w", operation, err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return codexCredential{}, fmt.Errorf("OpenAI token %s response is missing required fields", operation)
	}
	accountID, err := codexAccountIDFromJWT(token.AccessToken)
	if err != nil {
		return codexCredential{}, err
	}
	return codexCredential{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ExpiresAt: auth.now().Add(time.Duration(token.ExpiresIn) * time.Second).UnixMilli(), AccountID: accountID,
	}, nil
}

func (auth *codexAuthManager) do(ctx context.Context, method, endpoint, contentType string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	return auth.client.Do(request)
}

func responseError(prefix string, response *http.Response) error {
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return fmt.Errorf("%s failed (%d): %s", prefix, response.StatusCode, strings.TrimSpace(string(payload)))
}

func (auth *codexAuthManager) loadLocked() (codexCredential, error) {
	document, err := auth.loadAuthFileLocked()
	if err != nil {
		return codexCredential{}, err
	}
	raw := document.Providers[codexProviderID]
	if len(raw) == 0 {
		return codexCredential{}, os.ErrNotExist
	}
	var credential codexCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return codexCredential{}, fmt.Errorf("decode Codex credentials: %w", err)
	}
	if credential.RefreshToken == "" && credential.AccessToken == "" {
		return codexCredential{}, errors.New("Codex credential is empty")
	}
	return credential, nil
}

func (auth *codexAuthManager) saveLocked(credential codexCredential) error {
	document, err := auth.loadAuthFileLocked()
	if errors.Is(err, os.ErrNotExist) {
		document = providerAuthFile{Version: 2, Providers: make(map[string]json.RawMessage)}
	} else if err != nil {
		return err
	}
	if document.Providers == nil {
		document.Providers = make(map[string]json.RawMessage)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	document.Providers[codexProviderID] = raw
	return auth.saveAuthFileLocked(document)
}

func jwtPayload(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("token is not a JWT")
	}
	return base64.RawURLEncoding.DecodeString(parts[1])
}
