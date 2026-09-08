package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestBrowserURL(t *testing.T) {
	for _, value := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/html,hi", "https://user:pass@example.com", "/relative", "http://"} {
		if _, err := browserURL(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"http://localhost:3000", "https://example.com/path?q=test", "http://[::1]:3000"} {
		if _, err := browserURL(value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserSafetyOnlyClassifiesNavigate(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"allowed":true,"reason":"authorized URL","notificationSummary":"This browser opens a local test page.","authorization":"none","effectScopes":[]}`}
	gate := newSafetyGate(provider)
	for _, tool := range []string{"browser_type", "browser_press_key", "browser_navigate", "browser_click", "browser_place_cursor", "browser_click_cursor", "browser_scroll", "browser_screenshot", "browser_close"} {
		decision := gate.Check(t.Context(), agent.GuardRequest{Tool: tool, Arguments: map[string]any{"url": "http://localhost:3000", "target": "e1"}})
		if !decision.Allowed {
			t.Fatalf("%s: %+v", tool, decision)
		}
	}
	if len(provider.requests) != 1 {
		t.Fatalf("classifier requests=%d", len(provider.requests))
	}
	if !strings.Contains(provider.requests[0].Messages[0].Content[0].Text, "without further classifier checks") {
		t.Fatal("browser risk context missing")
	}
	decision := gate.Check(t.Context(), agent.GuardRequest{Tool: "browser_navigate", Arguments: map[string]any{"url": "file:///etc/passwd"}})
	if decision.Allowed || len(provider.requests) != 1 {
		t.Fatal("invalid URL should be denied locally")
	}
}

func TestBrowserNetworkWait(t *testing.T) {
	network := &browserNetwork{}
	start := time.Now()
	idle, err := network.wait(t.Context(), time.Second)
	if err != nil || !idle || time.Since(start) < 500*time.Millisecond {
		t.Fatalf("idle=%v err=%v", idle, err)
	}
	network.update(nil, true)
	start = time.Now()
	idle, err = network.wait(t.Context(), 75*time.Millisecond)
	if err != nil || idle || time.Since(start) > time.Second {
		t.Fatalf("timeout idle=%v err=%v", idle, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := network.wait(ctx, time.Second); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
}

func TestBrowserScreenshotDoesNotLaunch(t *testing.T) {
	session := &browserSession{}
	defer session.close()
	if _, err := session.execute("screenshot")(t.Context(), nil, nil); err == nil {
		t.Fatal("screenshot opened an unrequested page")
	}
	if session.driver != nil {
		t.Fatal("driver started without navigation")
	}
	for _, tool := range session.tools() {
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil || bytes.Contains(encoded, []byte(`"required":null`)) {
			t.Fatalf("invalid tool schema: %s (%v)", encoded, err)
		}
		if !tool.Serial {
			t.Fatalf("%s is not serial", tool.Name)
		}
	}
}

func TestBrowserRuntimeRegistrationAndWorkspaceChange(t *testing.T) {
	provider := &safetyTestProvider{}
	core, err := agent.New(agent.Config{Model: "test", Provider: provider, WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime("test", core, nil)
	defer runtime.close()
	runtime.bindCron(nil)
	original := runtime.browser
	if err := runtime.setCWD(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if runtime.browser != original || runtime.browser.driver != nil {
		t.Fatal("workspace change should keep the browser owner without launching it")
	}
	tools := runtime.tools(t.TempDir())
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"read", "write", "bash", "browser_navigate", "browser_click", "browser_scroll", "browser_screenshot", "browser_close"} {
		if !names[name] {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestBrowserImagesReachProviders(t *testing.T) {
	messages := []agent.Message{
		{Role: agent.RoleToolResult, ToolName: "browser_screenshot", ToolCallID: "one", Content: []agent.ContentBlock{{Type: "image", MIMEType: "image/png", Data: "aGVsbG8="}}},
		{Role: agent.RoleToolResult, ToolName: "read", ToolCallID: "two", Content: []agent.ContentBlock{{Type: "text", Text: "done"}}},
	}
	codex, err := convertCodexMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(codex)
	if !bytes.Contains(encoded, []byte("data:image/png;base64,aGVsbG8=")) {
		t.Fatal("Codex dropped image")
	}
	qwen, err := convertQwenMessages("", messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(qwen) != 3 || qwen[0].Role != "tool" || qwen[1].Role != "tool" || qwen[2].Role != "user" {
		t.Fatalf("tool result ordering: %#v", qwen)
	}
	encoded, _ = json.Marshal(qwen)
	if !bytes.Contains(encoded, []byte("data:image/png;base64,aGVsbG8=")) {
		t.Fatal("Qwen dropped image")
	}
}

// Explicit opt-in: requires Chromium installed via install-browser.sh.
func TestBrowserHeadlessIntegration(t *testing.T) {
	if os.Getenv("FORGE_BROWSER_TEST") != "1" {
		t.Skip("set FORGE_BROWSER_TEST=1 to run headless Chromium")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Browser test</title><button onclick="document.title='Clicked';this.textContent='Done'">Click me</button><a href="/slow">Slow page</a></html>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Loading</title><button onclick="fetch('/hang')">Fetch forever</button><script>fetch('/hang')</script></html>`)
	})
	mux.HandleFunc("/keyboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><form onsubmit="event.preventDefault();document.title='Submitted: '+document.querySelector('input').value"><input aria-label="Search terms"><button>Submit</button></form><script>window.keyCount=0;document.addEventListener('keydown',()=>window.keyCount++);</script></html>`)
	})
	mux.HandleFunc("/cursor", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><style>#box{position:absolute;left:100px;top:100px;width:150px;height:100px;background:gray}#box:hover{background:green}</style><div id="box" onmouseenter="setTimeout(()=>document.title='Hovered',50)" onclick="document.title='Left clicked'" oncontextmenu="event.preventDefault();document.title='Right clicked'">Hover target</div></html>`)
	})
	mux.HandleFunc("/scroll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><style>body{margin:0;height:2400px}button{position:absolute;top:1100px}</style><button onclick="document.title='Below fold clicked'">Below fold</button></html>`)
	})
	mux.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	server := httptest.NewServer(mux)
	defer server.Close()
	session := &browserSession{}
	defer session.close()
	call := func(action string, args map[string]any) agent.ToolResult {
		t.Helper()
		result, err := session.execute(action)(t.Context(), args, nil)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if action != "close" {
			if len(result.Content) != 2 || result.Content[1].Type != "image" {
				t.Fatalf("missing image: %#v", result)
			}
			data, err := base64.StdEncoding.DecodeString(result.Content[1].Data)
			if err != nil {
				t.Fatal(err)
			}
			image, err := png.Decode(bytes.NewReader(data))
			if err != nil || image.Bounds().Dx() != 1280 {
				t.Fatalf("invalid screenshot: %v", err)
			}
		}
		return result
	}
	findRef := func(result agent.ToolResult, label string) string {
		t.Helper()
		for _, line := range strings.Split(result.Content[0].Text, "\n") {
			if strings.Contains(line, label) && strings.HasPrefix(line, "e") {
				return strings.SplitN(line, ":", 2)[0]
			}
		}
		t.Fatalf("missing reference %q: %s", label, result.Content[0].Text)
		return ""
	}
	result := call("navigate", map[string]any{"url": server.URL})
	ref := findRef(result, "Click me")
	result = call("click", map[string]any{"target": ref})
	if !strings.Contains(result.Content[0].Text, "Title: Clicked") {
		t.Fatal(result.Content[0].Text)
	}
	if _, err := session.execute("click")(t.Context(), map[string]any{"target": ref}, nil); err == nil {
		t.Fatal("stale reference accepted")
	}
	if _, err := session.page.Evaluate(`document.cookie='session_test=one'`); err != nil {
		t.Fatal(err)
	}
	other := &browserSession{}
	defer other.close()
	if _, err := other.execute("navigate")(t.Context(), map[string]any{"url": server.URL}, nil); err != nil {
		t.Fatal(err)
	}
	cookie, err := other.page.Evaluate(`document.cookie`)
	if err != nil || strings.Contains(fmt.Sprint(cookie), "session_test") {
		t.Fatalf("session cookie leak: %v %v", cookie, err)
	}
	other.close()
	result = call("navigate", map[string]any{"url": server.URL + "/keyboard"})
	call("click", map[string]any{"target": findRef(result, "Search terms")})
	call("type", map[string]any{"text": "Hello 世界"})
	text, err := session.page.Locator("input").InputValue()
	if err != nil || text != "Hello 世界" {
		t.Fatalf("typed=%q err=%v", text, err)
	}
	call("press_key", map[string]any{"key": "ControlOrMeta+A"})
	call("type", map[string]any{"text": "replacement"})
	call("press_key", map[string]any{"key": "Backspace"})
	text, err = session.page.Locator("input").InputValue()
	if err != nil || text != "replacemen" {
		t.Fatalf("edited=%q err=%v", text, err)
	}
	call("press_key", map[string]any{"key": "Tab"})
	focus, err := session.page.Evaluate(`document.activeElement.tagName`)
	if err != nil || focus != "BUTTON" {
		t.Fatalf("focus=%v err=%v", focus, err)
	}
	result = call("press_key", map[string]any{"key": "Enter"})
	if !strings.Contains(result.Content[0].Text, "Title: Submitted: replacemen") {
		t.Fatal(result.Content[0].Text)
	}
	count, err := session.page.Evaluate(`window.keyCount`)
	if err != nil || count.(int) < 5 {
		t.Fatalf("keydown events=%v err=%v", count, err)
	}
	call("navigate", map[string]any{"url": server.URL + "/cursor"})
	if _, err := session.execute("click_cursor")(t.Context(), map[string]any{"button": "left"}, nil); err == nil {
		t.Fatal("clicked without placing cursor")
	}
	result = call("place_cursor", map[string]any{"x": 150, "y": 150})
	if !strings.Contains(result.Content[0].Text, "Title: Hovered") || !strings.Contains(result.Content[0].Text, "red crosshair") {
		t.Fatal(result.Content[0].Text)
	}
	result = call("click_cursor", map[string]any{"button": "left"})
	if !strings.Contains(result.Content[0].Text, "Title: Left clicked") {
		t.Fatal(result.Content[0].Text)
	}
	call("place_cursor", map[string]any{"x": 150, "y": 150})
	result = call("click_cursor", map[string]any{"button": "right"})
	if !strings.Contains(result.Content[0].Text, "Title: Right clicked") {
		t.Fatal(result.Content[0].Text)
	}
	if _, err := session.execute("place_cursor")(t.Context(), map[string]any{"x": -1, "y": 150}, nil); err == nil {
		t.Fatal("invalid coordinate accepted")
	}
	result = call("navigate", map[string]any{"url": server.URL + "/scroll"})
	if strings.Contains(result.Content[0].Text, "Below fold") {
		t.Fatal("offscreen target was exposed")
	}
	result = call("scroll", map[string]any{"direction": "down"})
	if !strings.Contains(result.Content[0].Text, "to y=720") {
		t.Fatal(result.Content[0].Text)
	}
	ref = findRef(result, "Below fold")
	result = call("click", map[string]any{"target": ref})
	if !strings.Contains(result.Content[0].Text, "Below fold clicked") {
		t.Fatal(result.Content[0].Text)
	}
	result = call("scroll", map[string]any{"direction": "up"})
	if !strings.Contains(result.Content[0].Text, "to y=0") {
		t.Fatal(result.Content[0].Text)
	}
	result = call("scroll", map[string]any{"direction": "up"})
	if !strings.Contains(result.Content[0].Text, "Page did not move") {
		t.Fatal("boundary not reported")
	}
	if _, err := session.execute("scroll")(t.Context(), map[string]any{"direction": "left"}, nil); err == nil {
		t.Fatal("invalid scroll direction accepted")
	}
	start := time.Now()
	result = call("navigate", map[string]any{"url": server.URL + "/slow"})
	if !strings.Contains(result.Content[0].Text, "10-second") || time.Since(start) > 14*time.Second {
		t.Fatalf("idle cap failed (%s): %s", time.Since(start), result.Content[0].Text)
	}
	start = time.Now()
	result = call("screenshot", nil)
	if time.Since(start) > 2*time.Second || !strings.Contains(result.Content[0].Text, "Immediate snapshot") {
		t.Fatal("screenshot waited for network idle")
	}
	start = time.Now()
	result = call("click", map[string]any{"target": findRef(result, "Fetch forever")})
	if !strings.Contains(result.Content[0].Text, "10-second") || time.Since(start) > 14*time.Second {
		t.Fatal("click idle cap failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if _, err := session.execute("navigate")(ctx, map[string]any{"url": server.URL + "/slow"}, nil); err == nil {
		t.Fatal("cancelled navigation succeeded")
	}
	if session.driver != nil {
		t.Fatal("cancelled browser not cleaned up")
	}
	call("close", nil)
}
