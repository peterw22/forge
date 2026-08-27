package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultClassifierModel = "gpt-5.6-luna"

type classifierSettingsFile struct {
	Version int    `json:"version"`
	Model   string `json:"model"`
}

type classifierSettings struct {
	mu    sync.RWMutex
	path  string
	model string
}

func newClassifierSettings() (*classifierSettings, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory for classifier settings: %w", err)
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	settings := &classifierSettings{
		path:  filepath.Join(configDir, "classifier.json"),
		model: defaultClassifierModel,
	}
	payload, err := os.ReadFile(settings.path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read classifier settings: %w", err)
	}
	info, err := os.Stat(settings.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("classifier settings must be a regular file with mode 0600 or stricter")
	}
	var document classifierSettingsFile
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("decode classifier settings: %w", err)
	}
	if document.Version != 1 || !validClassifierModelID(document.Model) {
		return nil, errors.New("classifier settings contain an invalid model")
	}
	settings.model = strings.TrimSpace(document.Model)
	return settings, nil
}

func (settings *classifierSettings) Model() string {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	return settings.model
}

func (settings *classifierSettings) SetModel(model string, available []modelInfo) error {
	model = strings.TrimSpace(model)
	if !validClassifierModelID(model) {
		return errors.New("classifier model is invalid")
	}
	found := false
	for _, item := range available {
		if item.ID == model {
			found = true
			break
		}
	}
	if !found {
		return errors.New("classifier model is not configured")
	}
	settings.mu.Lock()
	defer settings.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(settings.path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(settings.path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(classifierSettingsFile{Version: 1, Model: model}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := settings.path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, settings.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	settings.model = model
	return nil
}

func validClassifierModelID(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 200 || strings.ContainsAny(model, "\r\n\t ") {
		return false
	}
	for _, character := range model {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') &&
			!strings.ContainsRune("-._/:", character) {
			return false
		}
	}
	return true
}
