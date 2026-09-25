package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgySetupStatusNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	response := agySetupStatus(context.Background(), t.TempDir())
	if !response.Success || response.AgyInstalled == nil || *response.AgyInstalled || response.AgyReady == nil || *response.AgyReady || !strings.Contains(response.AgySetupCommand, "Install agy") {
		t.Fatalf("setup response=%#v", response)
	}
}

func TestAgySetupStatusSignedOut(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "agy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Please sign in' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	home := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	response := agySetupStatus(context.Background(), t.TempDir())
	if response.AgyInstalled == nil || !*response.AgyInstalled || response.AgyReady == nil || *response.AgyReady || !strings.Contains(response.AgySetupCommand, "HOME='"+home+"' agy") || !strings.Contains(response.AgySetupMessage, "sign-in required") {
		t.Fatalf("setup response=%#v", response)
	}
}
