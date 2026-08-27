package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID}})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestCodexAuthDeviceLoginPersistsAndRefreshes(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/usercode":
			_ = json.NewEncoder(writer).Encode(map[string]any{"device_auth_id": "device-1", "user_code": "ABCD-1234", "interval": 0})
		case "/device":
			_ = json.NewEncoder(writer).Encode(map[string]string{"authorization_code": "code-1", "code_verifier": "verifier-1"})
		case "/token":
			_ = request.ParseForm()
			count := tokenRequests.Add(1)
			if count == 1 {
				if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("redirect_uri") != openAICodexDeviceRedirectURI {
					t.Fatalf("unexpected exchange form: %v", request.Form)
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": testJWT("account-1"), "refresh_token": "refresh-1", "expires_in": 1})
				return
			}
			if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "refresh-1" {
				t.Fatalf("unexpected refresh form: %v", request.Form)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": testJWT("account-1"), "refresh_token": "refresh-2", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	auth := newCodexAuthManagerAt(path, codexAuthEndpoints{
		userCode: server.URL + "/usercode", device: server.URL + "/device",
		token: server.URL + "/token", verify: "https://example.test/device",
	}, server.Client())
	var notification codexDeviceCode
	status, err := auth.LoginDeviceCode(t.Context(), func(code codexDeviceCode) { notification = code })
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.AccountID != "account-1" || notification.UserCode != "ABCD-1234" {
		t.Fatalf("status=%+v notification=%+v", status, notification)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode=%o", info.Mode().Perm())
	}
	payload, _ := os.ReadFile(path)
	if !strings.Contains(string(payload), "refresh-1") {
		t.Fatal("refresh token was not persisted")
	}

	// The one-second access token is inside the five-minute refresh window.
	token, err := auth.Token("gpt-5.6-terra")
	if err != nil {
		t.Fatal(err)
	}
	if token != testJWT("account-1") || tokenRequests.Load() != 2 {
		t.Fatalf("token refresh failed: requests=%d", tokenRequests.Load())
	}
	if err := auth.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential still exists: %v", err)
	}
}

func TestCodexAuthUsesUnexpiredStoredToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	auth := newCodexAuthManagerAt(path, defaultCodexAuthEndpoints, http.DefaultClient)
	auth.now = func() time.Time { return time.Unix(1000, 0) }
	credential := codexCredential{AccessToken: testJWT("account-2"), RefreshToken: "refresh", ExpiresAt: time.Unix(2000, 0).UnixMilli(), AccountID: "account-2"}
	auth.mu.Lock()
	if err := auth.saveLocked(credential); err != nil {
		t.Fatal(err)
	}
	auth.mu.Unlock()
	token, err := auth.Token("model")
	if err != nil || token != credential.AccessToken {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

func TestCodexLogoutPreservesQwenConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	auth := newCodexAuthManagerAt(path, defaultCodexAuthEndpoints, http.DefaultClient)
	auth.mu.Lock()
	if err := auth.saveLocked(codexCredential{AccessToken: testJWT("account"), RefreshToken: "refresh", AccountID: "account"}); err != nil {
		auth.mu.Unlock()
		t.Fatal(err)
	}
	auth.mu.Unlock()
	if _, err := auth.SetQwenConfig(qwenConfig{APIKey: "qwen-key", Protocol: "openai", OpenAIBaseURL: defaultQwenOpenAIBaseURL, AnthropicBaseURL: defaultQwenAnthropicBaseURL}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(); err != nil {
		t.Fatal(err)
	}
	qwen, err := auth.QwenRuntimeConfig()
	if err != nil || qwen.APIKey != "qwen-key" {
		t.Fatalf("Qwen config lost after OpenAI logout: %+v err=%v", qwen, err)
	}
	status, err := auth.Status()
	if err != nil || status.Authenticated {
		t.Fatalf("OpenAI remained signed in: %+v err=%v", status, err)
	}
}
