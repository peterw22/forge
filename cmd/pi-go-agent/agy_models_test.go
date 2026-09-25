package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAgyModels(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\nprintf 'gemini-3.8-flash-high\\tGemini 3.8 Flash (High)\\nclaude-sonnet-4-6\\tClaude Sonnet 4.6\\ninvalid/model\\tInvalid\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	models, err := discoverAgyModels(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "agy/gemini-3.8-flash-high" {
		t.Fatalf("models = %#v", models)
	}
}

func TestDiscoverAgyModelsFailsClosed(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'Fetching available models...'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverAgyModels(context.Background(), binary); err == nil {
		t.Fatal("accepted unparseable model list")
	}
}
