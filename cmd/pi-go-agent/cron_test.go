package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peterw22/forge/internal/agent"
)

func TestCronManagerPersistsAndIsolatesJobsBySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron", "jobs.json")
	manager, err := newCronManagerAt(path, func(cronJobRecord) string { return "started" })
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.create("session-one", "*/5 * * * *", "UTC", "Review the repository.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.create("session-two", "15 * * * *", "America/New_York", "Summarize status.")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || second.ID != 2 {
		t.Fatalf("IDs = %d, %d", first.ID, second.ID)
	}
	if jobs := manager.list("session-one"); len(jobs) != 1 || jobs[0].ID != first.ID || jobs[0].NextRunAt == nil {
		t.Fatalf("session-one jobs = %#v", jobs)
	}
	if err := manager.delete("session-one", second.ID); err == nil {
		t.Fatal("session deleted another session's cron job")
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cron file mode=%v err=%v", info.Mode().Perm(), err)
	}

	reloaded, err := newCronManagerAt(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.close()
	if jobs := reloaded.list("session-two"); len(jobs) != 1 || jobs[0].ID != second.ID {
		t.Fatalf("reloaded jobs = %#v", jobs)
	}
	third, err := reloaded.create("session-one", "0 9 * * 1-5", "Europe/London", "Daily review.")
	if err != nil || third.ID != 3 {
		t.Fatalf("third=%#v err=%v", third, err)
	}
}

func TestCronToolCannotTargetOrDeleteAnotherSession(t *testing.T) {
	manager, err := newCronManagerAt(filepath.Join(t.TempDir(), "jobs.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.close()
	one := cronTool(manager, "session-one")
	two := cronTool(manager, "session-two")
	created, err := one.Execute(context.Background(), map[string]any{
		"action": "create", "schedule": "* * * * *", "timezone": "UTC", "prompt": "Check status.",
	}, func(agent.ToolResult) {})
	if err != nil {
		t.Fatal(err)
	}
	details := created.Details.(map[string]any)
	job := details["job"].(cronJobInfo)
	listed, err := two.Execute(context.Background(), map[string]any{"action": "list"}, func(agent.ToolResult) {})
	if err != nil || strings.Contains(listed.Content[0].Text, `"id": 1`) {
		t.Fatalf("other session list=%s err=%v", listed.Content[0].Text, err)
	}
	if _, err := two.Execute(context.Background(), map[string]any{"action": "delete", "id": float64(job.ID)}, func(agent.ToolResult) {}); err == nil {
		t.Fatal("other session deleted cron job")
	}
}

func TestCronFireRecordsBusySkipWithoutQueue(t *testing.T) {
	manager, err := newCronManagerAt(filepath.Join(t.TempDir(), "jobs.json"), func(cronJobRecord) string { return "skipped_busy" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.close()
	job, err := manager.create("session-one", "* * * * *", "UTC", "Check status.")
	if err != nil {
		t.Fatal(err)
	}
	manager.fire(job.ID)
	listed := manager.list("session-one")
	if len(listed) != 1 || listed[0].LastStatus != "skipped_busy" || listed[0].LastAttemptAt == nil {
		t.Fatalf("listed=%#v", listed)
	}
}

func TestCronValidationRequiresFiveFieldsTimezoneAndPrompt(t *testing.T) {
	for _, test := range []struct{ schedule, timezone, prompt string }{
		{"* * * * * *", "UTC", "run"},
		{"* * * * *", "", "run"},
		{"* * * * *", "Not/AZone", "run"},
		{"* * * * *", "UTC", ""},
	} {
		if err := validateCronJob(test.schedule, test.timezone, test.prompt); err == nil {
			t.Fatalf("invalid cron accepted: %#v", test)
		}
	}
	if err := validateCronJob("0 9 * * 1-5", "Asia/Singapore", "Send a report."); err != nil {
		t.Fatal(err)
	}
}

func TestCronCreateDeleteAreClassifiedButListIsNot(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"allowed":true,"reason":"authorized schedule update","notificationSummary":"This scheduled operation is confined to Forge.","authorization":"none","effectScopes":["manage Forge schedule"]}`}
	gate := newSafetyGate(provider)
	for _, request := range []agent.GuardRequest{
		{Tool: "cron", Arguments: map[string]any{"action": "list"}},
		{Tool: "cron", Arguments: map[string]any{"action": "create", "schedule": "* * * * *", "timezone": "UTC", "prompt": "review"}},
		{Tool: "cron", Arguments: map[string]any{"action": "delete", "id": float64(1)}},
	} {
		if decision := gate.Check(context.Background(), request); !decision.Allowed {
			t.Fatalf("decision=%#v", decision)
		}
	}
	if len(provider.requests) != 2 {
		t.Fatalf("classifier requests=%d, want 2", len(provider.requests))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(provider.requests[0].Messages[0].Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	operation := payload["operation"].(map[string]any)
	if operation["type"] != "cron" || operation["action"] != "create" {
		t.Fatalf("operation=%#v", operation)
	}
}

func TestCronRestartDoesNotRunCatchUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	past := time.Now().Add(-24 * time.Hour).UTC()
	file := cronJobFile{Version: cronFileVersion, NextID: 2, Jobs: []cronJobRecord{{ID: 1, SessionID: "session", Schedule: "0 0 * * *", Timezone: "UTC", Prompt: "run", CreatedAt: past, LastAttemptAt: &past}}}
	encoded, _ := json.Marshal(file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	runs := 0
	manager, err := newCronManagerAt(path, func(cronJobRecord) string { runs++; return "started" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.close()
	time.Sleep(50 * time.Millisecond)
	if runs != 0 {
		t.Fatalf("restart catch-up runs=%d", runs)
	}
}
