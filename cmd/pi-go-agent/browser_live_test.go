package main

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/peterw22/forge/internal/agent"
)

func TestBrowserFrameQueueCoalescesAndPrioritizes(t *testing.T) {
	s := newSubscriber(nil)
	defer s.close()
	for i := 1; i <= 100; i++ {
		s.enqueueFrame(backendResponse{Type: "browser_frame", BrowserFrame: &browserViewFrame{Sequence: uint64(i)}})
	}
	if len(s.frames) != 1 || len(s.responses) != 0 {
		t.Fatal("frames used reliable queue")
	}
	s.enqueue(backendResponse{Type: "response", ID: "approval"})
	first, ok := s.nextOutbound()
	if !ok || first.response.ID != "approval" {
		t.Fatal("reliable response not prioritized")
	}
	last, ok := s.nextOutbound()
	if !ok || last.response.BrowserFrame.Sequence != 100 {
		t.Fatal("did not keep newest frame")
	}
	s.enqueueFrame(backendResponse{Type: "browser_frame"})
	s.clearFrame()
	if len(s.frames) != 0 {
		t.Fatal("stale frame not cleared")
	}
}

func TestBrowserViewLeaseExpiry(t *testing.T) {
	s := newSubscriber(nil)
	defer s.close()
	live := browserLive{viewers: map[*subscriber]browserViewer{s: {expires: time.Now().Add(-time.Second)}}, owner: s, token: "secret", controlExpires: time.Now().Add(time.Second)}
	if !live.pruneLocked(time.Now()) || live.owner != nil || len(live.viewers) != 0 {
		t.Fatal("expired view retained control")
	}
	if err := live.watch(s, "missing"); err == nil {
		t.Fatal("subscribed to closed browser")
	}
}

func TestBrowserLiveCommandsRequireAttachedSession(t *testing.T) {
	core, err := agent.New(agent.Config{Model: "test", Provider: &safetyTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime("session-one", core, nil)
	defer runtime.close()
	subscriber := newSubscriber(nil)
	defer subscriber.close()
	client := &clientConnection{runtime: runtime, subscriber: subscriber}
	for _, kind := range []string{"browser_view_start", "browser_view_stop", "browser_control_acquire", "browser_control_release", "browser_input"} {
		if err := client.handle(backendCommand{Type: kind, Session: "session-two"}); err == nil {
			t.Fatalf("accepted cross-session %s", kind)
		}
	}
	if len(core.Snapshot().Messages) != 0 {
		t.Fatal("live commands entered transcript")
	}
}

func TestBrowserLiveHeadlessIntegration(t *testing.T) {
	if os.Getenv("FORGE_BROWSER_TEST") != "1" {
		t.Skip("set FORGE_BROWSER_TEST=1")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><input autofocus><button onclick="document.title='manual click'">Click</button></html>`))
	}))
	defer server.Close()
	browser := &browserSession{}
	defer browser.close()
	if _, err := browser.execute("navigate")(t.Context(), map[string]any{"url": server.URL}, nil); err != nil {
		t.Fatal(err)
	}
	state := browser.live.state()
	if !state.Open || state.Instance == "" {
		t.Fatal("browser state missing")
	}
	time.Sleep(600 * time.Millisecond)
	browser.live.mu.Lock()
	seq := browser.live.sequence
	browser.live.mu.Unlock()
	if seq != 0 {
		t.Fatal("captured without a viewer")
	}
	viewer, other := newSubscriber(nil), newSubscriber(nil)
	defer viewer.close()
	defer other.close()
	if err := browser.live.watch(viewer, state.Instance); err != nil {
		t.Fatal(err)
	}
	next := func() *browserViewFrame {
		t.Helper()
		select {
		case f := <-viewer.frames:
			frame := f.response.BrowserFrame
			browser.live.acknowledge(viewer, backendCommand{BrowserInstance: frame.Instance, FrameID: frame.Sequence})
			return frame
		case <-time.After(5 * time.Second):
			t.Fatal("no live frame")
			return nil
		}
	}
	frame := next()
	// A slow viewer cannot accumulate frames in its encrypted receive pipeline.
	time.Sleep(1200 * time.Millisecond)
	queued := <-viewer.frames
	pending := queued.response.BrowserFrame
	time.Sleep(1100 * time.Millisecond)
	if len(viewer.frames) != 0 {
		t.Fatal("sent another frame before display acknowledgement")
	}
	browser.live.acknowledge(viewer, backendCommand{BrowserInstance: pending.Instance, FrameID: pending.Sequence})
	data, err := base64.StdEncoding.DecodeString(frame.JPEG)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 1280 || img.Bounds().Dy() != 720 || frame.ContentWidth != 1152 || frame.ContentX != 64 {
		t.Fatalf("bad frame geometry: %+v", frame)
	}
	// Holding the entire browser action lock must not stop streaming.
	browser.mu.Lock()
	second := next()
	browser.mu.Unlock()
	if second.Sequence <= frame.Sequence {
		t.Fatal("frames frozen during action")
	}
	// A popup/dialog callback must never block Playwright's dispatcher.
	if _, err := browser.page.Evaluate(`window.open('about:blank'); 'opened'`); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.page.Evaluate(`alert('test'); 'dismissed'`); err != nil {
		t.Fatal(err)
	}
	frame = next()
	if frame.Sequence <= second.Sequence {
		t.Fatal("popup/dialog froze frames")
	}
	if err := browser.live.watch(other, state.Instance); err != nil {
		t.Fatal(err)
	}
	token, err := browser.control(viewer, backendCommand{BrowserInstance: state.Instance})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.control(other, backendCommand{BrowserInstance: state.Instance}); err == nil {
		t.Fatal("two owners")
	}
	if _, err := browser.execute("screenshot")(t.Context(), nil, nil); err == nil {
		t.Fatal("agent acted during manual control")
	}
	frame = next()
	cmd := backendCommand{BrowserInstance: state.Instance, ControlToken: token, FrameID: frame.Sequence, BrowserAction: "type", Text: "human text"}
	if err := browser.manualInput(other, cmd); err == nil {
		t.Fatal("nonowner input accepted")
	}
	if err := browser.manualInput(viewer, cmd); err != nil {
		t.Fatal(err)
	}
	value, err := browser.page.Locator("input").InputValue()
	if err != nil || value != "human text" {
		t.Fatalf("input=%q err=%v", value, err)
	}
	cmd.BrowserInstance = "stale"
	if err := browser.manualInput(viewer, cmd); err == nil {
		t.Fatal("stale input accepted")
	}
	browser.live.unwatch(viewer)
	if browser.live.manuallyControlled() {
		t.Fatal("control survived unsubscribe")
	}
	if len(browser.refs) != 0 {
		t.Fatal("manual input retained agent refs")
	}
	browser.live.unwatch(other)
	browser.close()
	if browser.live.state().Open {
		t.Fatal("close left browser open")
	}
}
