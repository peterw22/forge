package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSetupStatusNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	response := claudeSetupStatus(context.Background())
	if !response.Success || response.ClaudeInstalled == nil || *response.ClaudeInstalled || response.ClaudeReady == nil || *response.ClaudeReady || !strings.Contains(response.ClaudeSetupCommand, "Install Claude Code") {
		t.Fatalf("response=%#v", response)
	}
}
func TestClaudeSetupStatusSignedOut(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"loggedIn\":false}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	response := claudeSetupStatus(context.Background())
	if response.ClaudeInstalled == nil || !*response.ClaudeInstalled || response.ClaudeReady == nil || *response.ClaudeReady || response.ClaudeSetupCommand != "claude auth login" {
		t.Fatalf("response=%#v", response)
	}
}
func TestClaudeSetupStatusReady(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"loggedIn\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	response := claudeSetupStatus(context.Background())
	if response.ClaudeReady == nil || !*response.ClaudeReady {
		t.Fatalf("response=%#v", response)
	}
}
