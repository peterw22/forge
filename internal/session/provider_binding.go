package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ProviderBinding stores a provider's opaque conversation ID in the same
// append-only JSONL as the Pi Go transcript. No prompt text is duplicated.
type ProviderBinding struct {
	Type           string `json:"type"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	CWD            string `json:"cwd"`
	ConversationID string `json:"conversationId"`
}

func (controller *Controller) SetProviderBinding(binding ProviderBinding) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetProviderBinding(binding)
}
func (store *Store) SetProviderBinding(binding ProviderBinding) error {
	if binding.Provider != "agy" && binding.Provider != "claude" {
		return errors.New("invalid CLI provider binding")
	}
	if binding.ConversationID != "" && (!validProviderConversationID(binding.ConversationID) || binding.Model == "" || binding.CWD == "") {
		return errors.New("invalid provider conversation binding")
	}
	binding.Type = "provider_binding"
	return store.write(binding)
}
func validProviderConversationID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, c := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ReadProviderBinding reads the last binding for one provider; a tombstone
// clears it. The session path must come from Resolve, not from model output.
func ReadProviderBinding(path, provider string) (ProviderBinding, error) {
	file, err := os.Open(path)
	if err != nil {
		return ProviderBinding{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var binding ProviderBinding
	for scanner.Scan() {
		var row ProviderBinding
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.Type != "provider_binding" || row.Provider != provider {
			continue
		}
		if row.ConversationID != "" && !validProviderConversationID(row.ConversationID) {
			return ProviderBinding{}, fmt.Errorf("invalid persisted %s conversation ID", provider)
		}
		binding = row
	}
	if err := scanner.Err(); err != nil {
		return ProviderBinding{}, err
	}
	return binding, nil
}

// ProviderBindingForSession refuses to open arbitrary paths or follow a
// transcript from another workspace/session. Empty binding means fresh turn.
func ProviderBindingForSession(root, id, provider, model, cwd string) (ProviderBinding, error) {
	if root == "" || strings.ContainsAny(id, `/\\`) {
		return ProviderBinding{}, errors.New("invalid provider session root or ID")
	}
	path, err := Resolve(root, id)
	if err != nil {
		return ProviderBinding{}, err
	}
	binding, err := ReadProviderBinding(path, provider)
	if err != nil {
		return binding, err
	}
	if binding.ConversationID != "" && (binding.Model != model || binding.CWD != cwd) {
		return ProviderBinding{}, errors.New("provider session model or workspace changed; start a new session")
	}
	return binding, nil
}
