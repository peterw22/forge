package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/peterw22/pi-go/internal/agent"
)

const (
	cronFileVersion    = 1
	maxCronJobs        = 4096
	maxSessionCronJobs = 256
	maxCronPromptChars = 50000
)

type cronJobRecord struct {
	ID            uint64     `json:"id"`
	SessionID     string     `json:"sessionId"`
	Schedule      string     `json:"schedule"`
	Timezone      string     `json:"timezone"`
	Prompt        string     `json:"prompt"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastStatus    string     `json:"lastStatus,omitempty"`
}

type cronJobInfo struct {
	ID            uint64     `json:"id"`
	Schedule      string     `json:"schedule"`
	Timezone      string     `json:"timezone"`
	Prompt        string     `json:"prompt"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastStatus    string     `json:"lastStatus,omitempty"`
	NextRunAt     *time.Time `json:"nextRunAt,omitempty"`
}

type cronJobFile struct {
	Version int             `json:"version"`
	NextID  uint64          `json:"nextId"`
	Jobs    []cronJobRecord `json:"jobs"`
}

type cronManager struct {
	mu        sync.Mutex
	path      string
	scheduler gocron.Scheduler
	jobs      map[uint64]cronJobRecord
	handles   map[uint64]uuid.UUID
	nextID    uint64
	run       func(cronJobRecord) string
	now       func() time.Time
}

func newCronManager(run func(cronJobRecord) string) (*cronManager, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory for cron jobs: %w", err)
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	return newCronManagerAt(filepath.Join(configDir, "cron", "jobs.json"), run)
}

func newCronManagerAt(path string, run func(cronJobRecord) string) (*cronManager, error) {
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("create cron scheduler: %w", err)
	}
	manager := &cronManager{
		path: path, scheduler: scheduler, jobs: make(map[uint64]cronJobRecord),
		handles: make(map[uint64]uuid.UUID), nextID: 1, run: run, now: time.Now,
	}
	if err := manager.load(); err != nil {
		_ = scheduler.Shutdown()
		return nil, err
	}
	for id, job := range manager.jobs {
		if err := manager.registerLocked(id, job); err != nil {
			_ = scheduler.Shutdown()
			return nil, fmt.Errorf("register cron job %d: %w", id, err)
		}
	}
	// Starting after all persisted jobs are registered computes only their next
	// natural occurrence. gocron does not replay times missed while Forge was
	// stopped, matching the no-catch-up policy.
	scheduler.Start()
	return manager, nil
}

func (manager *cronManager) load() error {
	encoded, err := os.ReadFile(manager.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cron jobs: %w", err)
	}
	info, err := os.Stat(manager.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("cron jobs file must be a regular owner-only file")
	}
	if len(encoded) > 4<<20 {
		return errors.New("cron jobs file is too large")
	}
	var file cronJobFile
	if err := json.Unmarshal(encoded, &file); err != nil {
		return fmt.Errorf("decode cron jobs: %w", err)
	}
	if file.Version != cronFileVersion {
		return fmt.Errorf("unsupported cron jobs version %d", file.Version)
	}
	if len(file.Jobs) > maxCronJobs {
		return errors.New("cron job count exceeds limit")
	}
	for _, job := range file.Jobs {
		if job.ID == 0 || manager.jobs[job.ID].ID != 0 {
			return errors.New("cron jobs contain an invalid or duplicate ID")
		}
		if err := validateCronJob(job.Schedule, job.Timezone, job.Prompt); err != nil {
			return fmt.Errorf("cron job %d: %w", job.ID, err)
		}
		if strings.TrimSpace(job.SessionID) == "" {
			return fmt.Errorf("cron job %d has no owner session", job.ID)
		}
		manager.jobs[job.ID] = job
		if job.ID >= manager.nextID {
			manager.nextID = job.ID + 1
		}
	}
	if file.NextID > manager.nextID {
		manager.nextID = file.NextID
	}
	return nil
}

func (manager *cronManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(manager.path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(manager.path), 0o700); err != nil {
		return err
	}
	jobs := make([]cronJobRecord, 0, len(manager.jobs))
	for _, job := range manager.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	encoded, err := json.MarshalIndent(cronJobFile{Version: cronFileVersion, NextID: manager.nextID, Jobs: jobs}, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := manager.path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, manager.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validateCronJob(schedule, timezone, prompt string) error {
	schedule = strings.TrimSpace(schedule)
	if len(strings.Fields(schedule)) != 5 {
		return errors.New("schedule must contain exactly five cron fields")
	}
	if strings.ContainsAny(schedule, "\r\n") {
		return errors.New("schedule must be one line")
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("invalid IANA timezone: %w", err)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len([]rune(prompt)) > maxCronPromptChars {
		return fmt.Errorf("prompt must contain 1-%d characters", maxCronPromptChars)
	}
	return nil
}

func cronExpression(schedule, timezone string) string {
	return "CRON_TZ=" + timezone + " " + schedule
}

func (manager *cronManager) registerLocked(id uint64, record cronJobRecord) error {
	job, err := manager.scheduler.NewJob(
		gocron.CronJob(cronExpression(record.Schedule, record.Timezone), false),
		gocron.NewTask(func() { manager.fire(id) }),
		gocron.WithName("forge-cron-"+strconv.FormatUint(id, 10)),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		return err
	}
	manager.handles[id] = job.ID()
	return nil
}

func (manager *cronManager) fire(id uint64) {
	manager.mu.Lock()
	record, exists := manager.jobs[id]
	manager.mu.Unlock()
	if !exists {
		return
	}
	status := "session_unavailable"
	if manager.run != nil {
		status = manager.run(record)
	}
	now := manager.now().UTC()
	manager.mu.Lock()
	if current, stillExists := manager.jobs[id]; stillExists {
		current.LastAttemptAt = &now
		current.LastStatus = status
		manager.jobs[id] = current
		if err := manager.saveLocked(); err != nil {
			fmt.Fprintln(os.Stderr, "persist cron status:", err)
		}
	}
	manager.mu.Unlock()
}

func (manager *cronManager) list(owner string) []cronJobInfo {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]cronJobInfo, 0)
	jobsByID := make(map[uuid.UUID]gocron.Job)
	for _, job := range manager.scheduler.Jobs() {
		jobsByID[job.ID()] = job
	}
	for id, record := range manager.jobs {
		if record.SessionID != owner {
			continue
		}
		info := cronJobInfo{ID: id, Schedule: record.Schedule, Timezone: record.Timezone, Prompt: record.Prompt, CreatedAt: record.CreatedAt, LastAttemptAt: record.LastAttemptAt, LastStatus: record.LastStatus}
		if scheduled := jobsByID[manager.handles[id]]; scheduled != nil {
			if next, err := scheduled.NextRun(); err == nil && !next.IsZero() {
				next = next.UTC()
				info.NextRunAt = &next
			}
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (manager *cronManager) create(owner, schedule, timezone, prompt string) (cronJobInfo, error) {
	if strings.TrimSpace(owner) == "" {
		return cronJobInfo{}, errors.New("cron owner session is required")
	}
	if err := validateCronJob(schedule, timezone, prompt); err != nil {
		return cronJobInfo{}, err
	}
	record := cronJobRecord{SessionID: owner, Schedule: strings.TrimSpace(schedule), Timezone: strings.TrimSpace(timezone), Prompt: strings.TrimSpace(prompt), CreatedAt: manager.now().UTC()}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.jobs) >= maxCronJobs {
		return cronJobInfo{}, errors.New("cron job limit reached")
	}
	owned := 0
	for _, job := range manager.jobs {
		if job.SessionID == owner {
			owned++
		}
	}
	if owned >= maxSessionCronJobs {
		return cronJobInfo{}, errors.New("session cron job limit reached")
	}
	record.ID = manager.nextID
	manager.nextID++
	if err := manager.registerLocked(record.ID, record); err != nil {
		manager.nextID--
		return cronJobInfo{}, fmt.Errorf("schedule cron job: %w", err)
	}
	manager.jobs[record.ID] = record
	if err := manager.saveLocked(); err != nil {
		_ = manager.scheduler.RemoveJob(manager.handles[record.ID])
		delete(manager.handles, record.ID)
		delete(manager.jobs, record.ID)
		manager.nextID--
		return cronJobInfo{}, err
	}
	return cronJobInfo{ID: record.ID, Schedule: record.Schedule, Timezone: record.Timezone, Prompt: record.Prompt, CreatedAt: record.CreatedAt}, nil
}

func (manager *cronManager) delete(owner string, id uint64) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, exists := manager.jobs[id]
	if !exists || record.SessionID != owner {
		return errors.New("cron job not found")
	}
	if handle := manager.handles[id]; handle != uuid.Nil {
		if err := manager.scheduler.RemoveJob(handle); err != nil && !errors.Is(err, gocron.ErrJobNotFound) {
			return err
		}
	}
	delete(manager.jobs, id)
	delete(manager.handles, id)
	return manager.saveLocked()
}

func (manager *cronManager) close() error {
	if manager == nil || manager.scheduler == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return manager.scheduler.ShutdownWithContext(ctx)
}

func cronSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":   map[string]any{"type": "string", "enum": []string{"list", "create", "delete"}, "description": "List this session's jobs, create a job for this session, or delete one of this session's jobs."},
			"schedule": map[string]any{"type": "string", "description": "Required for create: standard five-field cron expression (minute hour day month weekday), minimum once per minute."},
			"timezone": map[string]any{"type": "string", "description": "Required for create: IANA timezone such as America/New_York or UTC."},
			"prompt":   map[string]any{"type": "string", "description": "Required for create: user turn sent automatically to the session that creates the job."},
			"id":       map[string]any{"type": "integer", "minimum": 1, "description": "Required for delete: numeric job ID owned by this session."},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func cronTool(manager *cronManager, owner string) agent.Tool {
	return agent.Tool{
		Name:        "cron",
		Description: "Manage durable Forge application cron jobs for this session only. list returns only this session's jobs. create requires a five-field schedule, IANA timezone, and prompt; when due it sends the prompt as a normal user turn to this same session. If the session is busy, that occurrence is skipped. Restarted agents do not catch up missed occurrences. delete requires this session's numeric job ID. This tool never accesses system crontab.",
		Parameters:  cronSchema(),
		Execute: func(_ context.Context, args map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
			if manager == nil {
				return agent.ToolResult{}, errors.New("cron manager is unavailable")
			}
			action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
			switch action {
			case "list":
				jobs := manager.list(owner)
				encoded, _ := json.MarshalIndent(map[string]any{"jobs": jobs}, "", "  ")
				return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: string(encoded)}}, Details: map[string]any{"action": action, "jobs": jobs}}, nil
			case "create":
				job, err := manager.create(owner, stringArg(args, "schedule"), stringArg(args, "timezone"), stringArg(args, "prompt"))
				if err != nil {
					return agent.ToolResult{}, err
				}
				return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: fmt.Sprintf("Created cron job %d for %s in %s", job.ID, job.Schedule, job.Timezone)}}, Details: map[string]any{"action": action, "job": job}}, nil
			case "delete", "deregister":
				id, err := uintArg(args, "id")
				if err != nil {
					return agent.ToolResult{}, err
				}
				if err := manager.delete(owner, id); err != nil {
					return agent.ToolResult{}, err
				}
				return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: fmt.Sprintf("Deleted cron job %d", id)}}, Details: map[string]any{"action": "delete", "id": id}}, nil
			default:
				return agent.ToolResult{}, errors.New("cron action must be list, create, or delete")
			}
		},
	}
}

func uintArg(args map[string]any, name string) (uint64, error) {
	switch value := args[name].(type) {
	case float64:
		if value >= 1 && value == float64(uint64(value)) {
			return uint64(value), nil
		}
	case int:
		if value >= 1 {
			return uint64(value), nil
		}
	case int64:
		if value >= 1 {
			return uint64(value), nil
		}
	case json.Number:
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		if err == nil && parsed >= 1 {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("%s must be a positive integer", name)
}
