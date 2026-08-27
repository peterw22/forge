package main

import (
	"os"
	"testing"
)

func TestClassifierSettingsRejectInsecurePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", dir)
	if err := os.WriteFile(dir+"/classifier.json", []byte(`{"version":1,"model":"gpt-5.6-sol"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newClassifierSettings(); err == nil {
		t.Fatal("insecure classifier settings permissions accepted")
	}
}

func TestValidClassifierModelID(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "openai-codex/gpt-5.6-sol", "qwen-code-plan/qwen3-coder"} {
		if !validClassifierModelID(model) {
			t.Fatalf("valid model rejected: %q", model)
		}
	}
	for _, model := range []string{"", "model with space", "model\nother", "model;rm"} {
		if validClassifierModelID(model) {
			t.Fatalf("invalid model accepted: %q", model)
		}
	}
}
