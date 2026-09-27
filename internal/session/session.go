package session

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/peterw22/forge/internal/agent"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Header struct {
	Type                 string `json:"type"`
	Version              int    `json:"version"`
	CWD, Model, Thinking string
	Name                 *string   `json:"name,omitempty"`
	YOLO                 bool      `json:"yolo"`
	CreatedAt            time.Time `json:"createdAt"`
}
type Store struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// Entry is the metadata needed by the session picker. ID is a UUIDv7, so
// sorting it lexicographically also sorts it by creation time.
type Entry struct {
	ID              string    `json:"id"`
	Path            string    `json:"-"`
	Name            *string   `json:"name"`
	LastMessageTime time.Time `json:"lastMessageTime"`
	Summary         string    `json:"summary"`
	CWD             string    `json:"cwd,omitempty"`
	Active          bool      `json:"active"`
}

type Controller struct {
	mu    sync.Mutex
	cwd   string
	store *Store
}

func NewController(cwd string, store *Store) *Controller { return &Controller{cwd: cwd, store: store} }
func (controller *Controller) Path() string {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.Path()
}
func (controller *Controller) Append(message agent.Message, usage agent.Usage) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.Append(message, usage)
}
func (controller *Controller) AppendTurnSummary(summary string, timestamp int64) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.AppendTurnSummary(summary, timestamp)
}
func (controller *Controller) AppendCompaction(result agent.CompactionResult) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.AppendCompaction(result)
}
func (controller *Controller) SetName(name *string) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetName(name)
}
func (controller *Controller) SetModel(model string) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetModel(model)
}
func (controller *Controller) SetThinking(thinking string) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetThinking(thinking)
}
func (controller *Controller) SetYOLO(enabled bool) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetYOLO(enabled)
}
func (controller *Controller) SetCWD(cwd string) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.store.SetCWD(cwd)
}
func (controller *Controller) List() ([]Entry, error) { return List(controller.cwd) }
func (controller *Controller) New(workspace, model, thinking string) (string, error) {
	next, err := newAt(controller.cwd, workspace, model, thinking)
	if err != nil {
		return "", err
	}
	controller.mu.Lock()
	previous := controller.store
	controller.store = next
	controller.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return strings.TrimSuffix(filepath.Base(next.path), ".jsonl"), nil
}
func (controller *Controller) Switch(id string) (Header, []agent.Message, agent.Usage, error) {
	path, err := Resolve(controller.cwd, id)
	if err != nil {
		return Header{}, nil, agent.Usage{}, err
	}
	next, header, messages, usage, err := Resume(path)
	if err != nil {
		return Header{}, nil, agent.Usage{}, err
	}
	controller.mu.Lock()
	previous := controller.store
	controller.store = next
	controller.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return header, messages, usage, nil
}
func (controller *Controller) Close() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.store == nil {
		return nil
	}
	return controller.store.Close()
}

func New(cwd, model, thinking string) (*Store, error) { return newAt(cwd, cwd, model, thinking) }
func NewAt(sessionRoot, workspace, model, thinking string) (*Store, error) {
	return newAt(sessionRoot, workspace, model, thinking)
}

// NewAtID persists a previously reserved session ID. It is used to promote a
// server's in-memory draft session only when the first prompt is submitted.
func NewAtID(sessionRoot, workspace, model, thinking, id string, yolo bool) (*Store, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("session id must be a UUIDv7")
	}
	return newAtID(sessionRoot, workspace, model, thinking, id, yolo)
}
func (s *Store) ID() string   { return strings.TrimSuffix(filepath.Base(s.path), ".jsonl") }
func (s *Store) Path() string { return s.path }
func newAt(sessionRoot, workspace, model, thinking string) (*Store, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return newAtID(sessionRoot, workspace, model, thinking, id, false)
}
func newAtID(sessionRoot, workspace, model, thinking, id string, yolo bool) (*Store, error) {
	dir := filepath.Join(sessionRoot, ".pi-go", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, id+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return nil, fmt.Errorf("session already exists: %s", id)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	s, err := open(p, Header{Type: "session", Version: 1, CWD: workspace, Model: model, Thinking: thinking, YOLO: yolo, CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	latest := filepath.Join(sessionRoot, ".pi-go", "latest")
	temporaryLatest := latest + "." + id + ".tmp"
	if err = os.WriteFile(temporaryLatest, []byte(p+"\n"), 0600); err != nil {
		_ = s.Close()
		_ = os.Remove(p)
		return nil, err
	}
	if err = os.Rename(temporaryLatest, latest); err != nil {
		_ = os.Remove(temporaryLatest)
		_ = s.Close()
		_ = os.Remove(p)
		return nil, err
	}
	return s, nil
}
func NewID() (string, error) {
	var v [16]byte
	if _, err := rand.Read(v[:]); err != nil {
		return "", err
	}
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		v[i] = byte(ms)
		ms >>= 8
	}
	v[6] = 0x70 | (v[6] & 15)
	v[8] = 0x80 | (v[8] & 63)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", binary.BigEndian.Uint32(v[:4]), binary.BigEndian.Uint16(v[4:6]), binary.BigEndian.Uint16(v[6:8]), binary.BigEndian.Uint16(v[8:10]), v[10:]), nil
}
func Latest(cwd string) (string, error) {
	b, e := os.ReadFile(filepath.Join(cwd, ".pi-go", "latest"))
	return strings.TrimSpace(string(b)), e
}
func Resolve(cwd, id string) (string, error) {
	if id == "latest" {
		return Latest(cwd)
	}
	if strings.ContainsAny(id, `/\\`) {
		return "", fmt.Errorf("session id must be a UUIDv7")
	}
	p := filepath.Join(cwd, ".pi-go", "sessions", id+".jsonl")
	_, e := os.Stat(p)
	return p, e
}

// List returns all project sessions newest first. Callers that render a
// bounded page should use ListPage so only the selected UUIDv7 files are read.
func List(cwd string) ([]Entry, error) {
	return ListPage(cwd, 0, 0)
}

// ListPage sorts UUIDv7 filenames before opening any session content, then
// reads metadata only for the requested files. A non-positive limit means all
// remaining sessions and is retained for terminal/full-list callers.
func ListPage(cwd string, offset, limit int) ([]Entry, error) {
	dir := filepath.Join(cwd, ".pi-go", "sessions")
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
			continue
		}
		names = append(names, file.Name())
	}
	// UUIDv7 textual order is chronological, so descending filenames are newest
	// first without opening or decoding every session file.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if offset < 0 {
		offset = 0
	}
	if offset > len(names) {
		offset = len(names)
	}
	end := len(names)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	entries := make([]Entry, 0, end-offset)
	for _, name := range names[offset:end] {
		id := strings.TrimSuffix(name, ".jsonl")
		path := filepath.Join(dir, name)
		entry := Entry{ID: id, Path: path}
		if info, statErr := os.Stat(path); statErr == nil {
			entry.LastMessageTime = info.ModTime()
		}
		if readErr := readEntry(path, &entry); readErr != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Count returns the number of persisted session JSONL files without opening
// their contents.
func Count(cwd string) (int, error) {
	dir := filepath.Join(cwd, ".pi-go", "sessions")
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".jsonl" {
			count++
		}
	}
	return count, nil
}

func readEntry(path string, entry *Entry) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var row struct {
			Type      string        `json:"type"`
			Name      *string       `json:"name"`
			CreatedAt time.Time     `json:"createdAt"`
			Timestamp int64         `json:"timestamp"`
			Summary   string        `json:"summary"`
			CWD       string        `json:"cwd"`
			Message   agent.Message `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		switch row.Type {
		case "session_settings":
			if row.CWD != "" {
				entry.CWD = row.CWD
			}
		case "session":
			entry.CWD = row.CWD
			entry.Name = row.Name
			if entry.LastMessageTime.IsZero() {
				entry.LastMessageTime = row.CreatedAt
			}
		case "session_name":
			entry.Name = row.Name
		case "turn_summary":
			if row.Timestamp > 0 {
				entry.LastMessageTime = time.UnixMilli(row.Timestamp)
			}
			if strings.TrimSpace(row.Summary) != "" {
				entry.Summary = strings.TrimSpace(row.Summary)
			}
		case "compaction":
			if entry.Summary == "" && row.Timestamp > 0 {
				entry.LastMessageTime = time.UnixMilli(row.Timestamp)
			}
		case "message":
			// Once a classifier summary exists, its timestamp represents the
			// completed turn and must not be replaced by older raw messages.
			if entry.Summary == "" && row.Message.Timestamp > 0 {
				entry.LastMessageTime = time.UnixMilli(row.Message.Timestamp)
			}
		}
	}
	return scanner.Err()
}

func Resume(path string) (*Store, Header, []agent.Message, agent.Usage, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, Header{}, nil, agent.Usage{}, e
	}
	defer f.Close()
	var h Header
	var ms []agent.Message
	var u agent.Usage
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for s.Scan() {
		line := s.Bytes()
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case "session":
			_ = json.Unmarshal(line, &h)
		case "session_name":
			var record struct {
				Name *string `json:"name"`
			}
			if json.Unmarshal(line, &record) == nil {
				h.Name = record.Name
			}
		case "session_settings":
			var record struct {
				Model    *string `json:"model"`
				Thinking *string `json:"thinking"`
				CWD      *string `json:"cwd"`
				YOLO     *bool   `json:"yolo"`
			}
			if json.Unmarshal(line, &record) == nil {
				if record.Model != nil && strings.TrimSpace(*record.Model) != "" {
					h.Model = *record.Model
				}
				if record.Thinking != nil && strings.TrimSpace(*record.Thinking) != "" {
					h.Thinking = *record.Thinking
				}
				if record.CWD != nil && strings.TrimSpace(*record.CWD) != "" {
					h.CWD = *record.CWD
				}
				if record.YOLO != nil {
					h.YOLO = *record.YOLO
				}
			}
		case "compaction":
			var result agent.CompactionResult
			if json.Unmarshal(line, &result) != nil {
				continue
			}
			ms = append([]agent.Message{{Role: agent.RoleCompactionSummary, Content: []agent.ContentBlock{{Type: "text", Text: result.Summary}}, Timestamp: result.Timestamp}}, result.RetainedTail...)
			u.Input += result.Usage.Input
			u.Output += result.Usage.Output
			u.CacheRead += result.Usage.CacheRead
			u.CacheWrite += result.Usage.CacheWrite
			u.TotalTokens += result.Usage.TotalTokens
		case "message":
			var record struct {
				Message agent.Message `json:"message"`
				Usage   agent.Usage   `json:"usage"`
			}
			if json.Unmarshal(line, &record) != nil {
				continue
			}
			ms = append(ms, record.Message)
			u.Input += record.Usage.Input
			u.Output += record.Usage.Output
			u.CacheRead += record.Usage.CacheRead
			u.CacheWrite += record.Usage.CacheWrite
			u.TotalTokens += record.Usage.TotalTokens
		}
	}
	if e = s.Err(); e != nil {
		return nil, Header{}, nil, agent.Usage{}, e
	}
	st, e := open(path, Header{})
	if e != nil {
		return nil, Header{}, nil, agent.Usage{}, e
	}
	for _, repaired := range repairDanglingToolCalls(ms) {
		ms = append(ms, repaired)
		if e = st.Append(repaired, agent.Usage{}); e != nil {
			_ = st.Close()
			return nil, Header{}, nil, agent.Usage{}, e
		}
	}
	return st, h, ms, u, nil
}

// A crash can occur after an assistant tool call is flushed but before its
// result is flushed. Responses APIs reject that dangling call on every future
// request, so resume closes it with a visible synthetic error result.
func repairDanglingToolCalls(messages []agent.Message) []agent.Message {
	pending := make(map[string]agent.ContentBlock)
	var order []string
	for _, message := range messages {
		if message.Role == agent.RoleAssistant {
			for _, block := range message.Content {
				if block.Type == "toolCall" && block.ID != "" {
					pending[block.ID] = block
					order = append(order, block.ID)
				}
			}
		}
		if message.Role == agent.RoleToolResult {
			delete(pending, message.ToolCallID)
		}
	}
	var repaired []agent.Message
	for _, id := range order {
		call, ok := pending[id]
		if !ok {
			continue
		}
		repaired = append(repaired, agent.Message{Role: agent.RoleToolResult, ToolCallID: id, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli(), Content: []agent.ContentBlock{{Type: "text", Text: "Tool execution was interrupted before a result was recorded."}}})
		delete(pending, id)
	}
	return repaired
}
func open(p string, h Header) (*Store, error) {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if e != nil {
		return nil, e
	}
	s := &Store{path: p, file: f}
	if h.Type != "" {
		e = s.write(h)
	}
	return s, e
}
func (s *Store) SetName(name *string) error {
	return s.write(struct {
		Type string  `json:"type"`
		Name *string `json:"name"`
	}{"session_name", name})
}
func (s *Store) SetModel(model string) error {
	return s.write(struct {
		Type  string `json:"type"`
		Model string `json:"model"`
	}{"session_settings", model})
}
func (s *Store) SetThinking(thinking string) error {
	return s.write(struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
	}{"session_settings", thinking})
}
func (s *Store) SetYOLO(enabled bool) error {
	return s.write(struct {
		Type string `json:"type"`
		YOLO bool   `json:"yolo"`
	}{"session_settings", enabled})
}
func (s *Store) SetCWD(cwd string) error {
	return s.write(struct {
		Type string `json:"type"`
		CWD  string `json:"cwd"`
	}{"session_settings", cwd})
}
func (s *Store) AppendTurnSummary(summary string, timestamp int64) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("turn summary must not be empty")
	}
	if timestamp <= 0 {
		timestamp = time.Now().UnixMilli()
	}
	return s.write(struct {
		Type      string `json:"type"`
		Summary   string `json:"summary"`
		Timestamp int64  `json:"timestamp"`
	}{"turn_summary", summary, timestamp})
}

func (s *Store) AppendCompaction(result agent.CompactionResult) error {
	return s.write(struct {
		Type string `json:"type"`
		agent.CompactionResult
	}{"compaction", result})
}
func (s *Store) Append(m agent.Message, u agent.Usage) error {
	return s.write(struct {
		Type    string        `json:"type"`
		Message agent.Message `json:"message"`
		Usage   agent.Usage   `json:"usage"`
	}{"message", m, u})
}
func (s *Store) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := json.NewEncoder(s.file).Encode(v); e != nil {
		return e
	}
	return s.file.Sync()
}
func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.file.Close() }
