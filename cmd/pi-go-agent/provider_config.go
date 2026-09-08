package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	legacyQwenProviderID        = "qwen-code-plan"
	qwenProviderID              = legacyQwenProviderID
	codexProviderID             = "openai-codex"
	apiProviderStoragePrefix    = "api:"
	defaultAPIProviderName      = "Qwen-Code-Plan"
	defaultQwenOpenAIBaseURL    = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	defaultQwenAnthropicBaseURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic"
)

var apiProviderNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)

type providerAuthFile struct {
	Version   int                        `json:"version"`
	Providers map[string]json.RawMessage `json:"providers"`
}

type apiConfig struct {
	APIKey           string   `json:"api_key"`
	Protocol         string   `json:"protocol"`
	OpenAIBaseURL    string   `json:"openai_base_url"`
	AnthropicBaseURL string   `json:"anthropic_base_url"`
	DefaultModel     string   `json:"default_model,omitempty"`
	Models           []string `json:"models,omitempty"`
}

type apiPublicConfig struct {
	Name             string   `json:"name"`
	Configured       bool     `json:"configured"`
	APIKeyConfigured bool     `json:"apiKeyConfigured"`
	Protocol         string   `json:"protocol"`
	OpenAIBaseURL    string   `json:"openaiBaseUrl"`
	AnthropicBaseURL string   `json:"anthropicBaseUrl"`
	DefaultModel     string   `json:"defaultModel,omitempty"`
	Models           []string `json:"models,omitempty"`
}

// Compatibility aliases keep existing local configuration tests and migration
// code readable while the public product language is now simply "API".
type qwenConfig = apiConfig
type qwenPublicConfig = apiPublicConfig

type modelInfo struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

func defaultAPIConfig() apiConfig {
	return apiConfig{Protocol: "openai", OpenAIBaseURL: defaultQwenOpenAIBaseURL, AnthropicBaseURL: defaultQwenAnthropicBaseURL}
}
func defaultQwenConfig() qwenConfig { return defaultAPIConfig() }

func validateAPIProviderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !apiProviderNamePattern.MatchString(name) || name == codexProviderID {
		return "", errors.New("API provider name must be 1-48 letters, numbers, hyphens, or underscores and start with a letter or number")
	}
	return name, nil
}
func apiStorageKey(name string) string { return apiProviderStoragePrefix + name }

func (auth *codexAuthManager) loadAuthFileLocked() (providerAuthFile, error) {
	payload, err := os.ReadFile(auth.path)
	if err != nil {
		return providerAuthFile{}, err
	}
	var document providerAuthFile
	if json.Unmarshal(payload, &document) == nil && document.Version >= 2 && document.Providers != nil {
		return document, nil
	}
	var legacy codexCredential
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return providerAuthFile{}, fmt.Errorf("decode provider credentials: %w", err)
	}
	if legacy.AccessToken == "" && legacy.RefreshToken == "" {
		return providerAuthFile{}, errors.New("credential file is empty")
	}
	raw, _ := json.Marshal(legacy)
	return providerAuthFile{Version: 2, Providers: map[string]json.RawMessage{codexProviderID: raw}}, nil
}

func (auth *codexAuthManager) saveAuthFileLocked(document providerAuthFile) error {
	if len(document.Providers) == 0 {
		if err := os.Remove(auth.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove provider credentials: %w", err)
		}
		return nil
	}
	document.Version = 3
	configDir := filepath.Dir(auth.path)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("create provider config directory: %w", err)
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return fmt.Errorf("protect provider config directory: %w", err)
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := auth.path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0600); err != nil {
		return fmt.Errorf("write provider credentials: %w", err)
	}
	if err := os.Chmod(temporary, 0600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, auth.path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("save provider credentials: %w", err)
	}
	return nil
}

func applyAPIDefaults(config *apiConfig) {
	if strings.TrimSpace(config.Protocol) == "" {
		config.Protocol = "openai"
	}
	if strings.TrimSpace(config.OpenAIBaseURL) == "" {
		config.OpenAIBaseURL = defaultQwenOpenAIBaseURL
	}
	if strings.TrimSpace(config.AnthropicBaseURL) == "" {
		config.AnthropicBaseURL = defaultQwenAnthropicBaseURL
	}
}
func applyQwenDefaults(config *qwenConfig) { applyAPIDefaults(config) }

func validateProviderBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("base URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("base URL must not include credentials, query parameters, or a fragment")
	}
	if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		return errors.New("non-loopback provider URLs must use HTTPS")
	}
	return nil
}

func normalizeAPIConfig(name string, config apiConfig) (apiConfig, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Protocol = strings.ToLower(strings.TrimSpace(config.Protocol))
	config.OpenAIBaseURL = strings.TrimRight(strings.TrimSpace(config.OpenAIBaseURL), "/")
	config.AnthropicBaseURL = strings.TrimRight(strings.TrimSpace(config.AnthropicBaseURL), "/")
	config.DefaultModel = strings.TrimSpace(strings.TrimPrefix(config.DefaultModel, name+"/"))
	if config.Protocol != "openai" && config.Protocol != "anthropic" {
		return apiConfig{}, errors.New("API protocol must be openai or anthropic")
	}
	if err := validateProviderBaseURL(config.OpenAIBaseURL); err != nil {
		return apiConfig{}, fmt.Errorf("OpenAI-compatible %w", err)
	}
	if err := validateProviderBaseURL(config.AnthropicBaseURL); err != nil {
		return apiConfig{}, fmt.Errorf("Anthropic-compatible %w", err)
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(config.Models))
	for _, model := range config.Models {
		model = strings.TrimSpace(strings.TrimPrefix(model, name+"/"))
		if model != "" && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	if config.DefaultModel != "" && !seen[config.DefaultModel] {
		models = append([]string{config.DefaultModel}, models...)
	}
	config.Models = models
	return config, nil
}
func normalizeQwenConfig(config qwenConfig) (qwenConfig, error) {
	return normalizeAPIConfig(legacyQwenProviderID, config)
}

func (auth *codexAuthManager) loadAPIConfigLocked(name string) (apiConfig, error) {
	config := defaultAPIConfig()
	document, err := auth.loadAuthFileLocked()
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return apiConfig{}, err
	}
	raw := document.Providers[apiStorageKey(name)]
	if len(raw) == 0 && (name == legacyQwenProviderID || name == defaultAPIProviderName) {
		raw = document.Providers[legacyQwenProviderID]
	}
	if len(raw) == 0 {
		return config, nil
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return apiConfig{}, fmt.Errorf("decode API provider %q: %w", name, err)
	}
	applyAPIDefaults(&config)
	return config, nil
}

func publicAPIConfig(name string, config apiConfig) apiPublicConfig {
	configured := config.APIKey != ""
	if name == legacyQwenProviderID || name == defaultAPIProviderName {
		configured = configured || strings.TrimSpace(os.Getenv("PI_GO_QWEN_API_KEY")) != ""
	}
	return apiPublicConfig{Name: name, Configured: configured, APIKeyConfigured: configured, Protocol: config.Protocol, OpenAIBaseURL: config.OpenAIBaseURL, AnthropicBaseURL: config.AnthropicBaseURL, DefaultModel: config.DefaultModel, Models: append([]string(nil), config.Models...)}
}

func (auth *codexAuthManager) APIConfigs() ([]apiPublicConfig, error) {
	auth.mu.Lock()
	defer auth.mu.Unlock()
	document, err := auth.loadAuthFileLocked()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for key := range document.Providers {
		if strings.HasPrefix(key, apiProviderStoragePrefix) {
			names[strings.TrimPrefix(key, apiProviderStoragePrefix)] = true
		}
	}
	if _, legacy := document.Providers[legacyQwenProviderID]; legacy {
		names[legacyQwenProviderID] = true
	}
	result := make([]apiPublicConfig, 0, len(names))
	for name := range names {
		config, loadErr := auth.loadAPIConfigFromDocument(name, document)
		if loadErr != nil {
			return nil, loadErr
		}
		result = append(result, publicAPIConfig(name, config))
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
}

func (auth *codexAuthManager) loadAPIConfigFromDocument(name string, document providerAuthFile) (apiConfig, error) {
	config := defaultAPIConfig()
	raw := document.Providers[apiStorageKey(name)]
	if len(raw) == 0 && name == legacyQwenProviderID {
		raw = document.Providers[legacyQwenProviderID]
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return apiConfig{}, fmt.Errorf("decode API provider %q: %w", name, err)
		}
	}
	applyAPIDefaults(&config)
	return config, nil
}

func (auth *codexAuthManager) APIConfig(name string) (apiPublicConfig, error) {
	name, err := validateAPIProviderName(name)
	if err != nil {
		return apiPublicConfig{}, err
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	config, err := auth.loadAPIConfigLocked(name)
	if err != nil {
		return apiPublicConfig{}, err
	}
	return publicAPIConfig(name, config), nil
}

func (auth *codexAuthManager) SetAPIConfig(name string, update apiConfig, preserveAPIKey, clearAPIKey bool) (apiPublicConfig, error) {
	name, err := validateAPIProviderName(name)
	if err != nil {
		return apiPublicConfig{}, err
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	current, err := auth.loadAPIConfigLocked(name)
	if err != nil {
		return apiPublicConfig{}, err
	}
	if preserveAPIKey {
		update.APIKey = current.APIKey
	}
	if clearAPIKey {
		update.APIKey = ""
	}
	update, err = normalizeAPIConfig(name, update)
	if err != nil {
		return apiPublicConfig{}, err
	}
	document, err := auth.loadAuthFileLocked()
	if errors.Is(err, os.ErrNotExist) {
		document = providerAuthFile{Version: 3, Providers: make(map[string]json.RawMessage)}
	} else if err != nil {
		return apiPublicConfig{}, err
	}
	if document.Providers == nil {
		document.Providers = make(map[string]json.RawMessage)
	}
	raw, _ := json.Marshal(update)
	document.Providers[apiStorageKey(name)] = raw
	if name == legacyQwenProviderID {
		delete(document.Providers, legacyQwenProviderID)
	}
	if err := auth.saveAuthFileLocked(document); err != nil {
		return apiPublicConfig{}, err
	}
	return publicAPIConfig(name, update), nil
}

func (auth *codexAuthManager) DeleteAPIConfig(name string) error {
	name, err := validateAPIProviderName(name)
	if err != nil {
		return err
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
	delete(document.Providers, apiStorageKey(name))
	if name == legacyQwenProviderID {
		delete(document.Providers, legacyQwenProviderID)
	}
	return auth.saveAuthFileLocked(document)
}

func (auth *codexAuthManager) APIRuntimeConfig(name string) (apiConfig, error) {
	name, err := validateAPIProviderName(name)
	if err != nil {
		return apiConfig{}, err
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	config, err := auth.loadAPIConfigLocked(name)
	if err != nil {
		return apiConfig{}, err
	}
	if name == legacyQwenProviderID || name == defaultAPIProviderName {
		if value := strings.TrimSpace(os.Getenv("PI_GO_QWEN_API_KEY")); value != "" {
			config.APIKey = value
		}
		if value := strings.TrimSpace(os.Getenv("PI_GO_QWEN_OPENAI_BASE_URL")); value != "" {
			config.OpenAIBaseURL = value
		}
		if value := strings.TrimSpace(os.Getenv("PI_GO_QWEN_ANTHROPIC_BASE_URL")); value != "" {
			config.AnthropicBaseURL = value
		}
		if value := strings.TrimSpace(os.Getenv("PI_GO_QWEN_PROTOCOL")); value != "" {
			config.Protocol = value
		}
	}
	config, err = normalizeAPIConfig(name, config)
	if err != nil {
		return apiConfig{}, err
	}
	if config.APIKey == "" {
		return apiConfig{}, fmt.Errorf("API provider %q key is not configured", name)
	}
	return config, nil
}

func qwenEndpoint(base, suffix string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, suffix) {
		return base
	}
	return base + suffix
}

func (auth *codexAuthManager) FetchAPIModels(ctx context.Context, name string) ([]modelInfo, error) {
	config, err := auth.APIRuntimeConfig(name)
	if err != nil {
		return nil, err
	}
	if config.Protocol != "openai" {
		return nil, errors.New("model discovery currently requires the OpenAI-compatible protocol")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, qwenEndpoint(config.OpenAIBaseURL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
	request.Header.Set("Accept", "application/json")
	response, err := auth.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query API models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, fmt.Errorf("API model query returned HTTP %d: %s", response.StatusCode, redactProviderSecret(strings.TrimSpace(string(payload)), config.APIKey))
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode API model list: %w", err)
	}
	models := make([]modelInfo, 0, len(result.Data))
	for _, item := range result.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, modelInfo{ID: name + "/" + id, Provider: name, Label: name + " · " + id})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func configuredModels(auth *codexAuthManager) ([]modelInfo, error) {
	codexModels := []string{
		"gpt-5.3-codex-spark",
		"gpt-5.5",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-6-astra",
	}
	models := make([]modelInfo, 0, len(codexModels)*2)
	for _, id := range codexModels {
		models = append(models,
			modelInfo{ID: id, Provider: codexProviderID, Label: "OpenAI Codex · " + id},
			modelInfo{ID: id + "-fast", Provider: codexProviderID, Label: "OpenAI Codex · " + id + " · Fast"},
		)
	}

	configs, err := auth.APIConfigs()
	if err != nil {
		return nil, err
	}
	for _, config := range configs {
		for _, id := range config.Models {
			models = append(models, modelInfo{ID: config.Name + "/" + id, Provider: config.Name, Label: config.Name + " · " + id})
		}
	}
	return models, nil
}

// Legacy wrappers migrate old single-provider installations without breaking
// command-line environments or existing tests.
func (auth *codexAuthManager) QwenConfig() (qwenPublicConfig, error) {
	return auth.APIConfig(legacyQwenProviderID)
}
func (auth *codexAuthManager) SetQwenConfig(update qwenConfig, preserveAPIKey, clearAPIKey bool) (qwenPublicConfig, error) {
	return auth.SetAPIConfig(legacyQwenProviderID, update, preserveAPIKey, clearAPIKey)
}
func (auth *codexAuthManager) QwenRuntimeConfig() (qwenConfig, error) {
	return auth.APIRuntimeConfig(legacyQwenProviderID)
}
func (auth *codexAuthManager) FetchQwenModels(ctx context.Context) ([]modelInfo, error) {
	return auth.FetchAPIModels(ctx, legacyQwenProviderID)
}
