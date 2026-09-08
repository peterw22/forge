package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	pw "github.com/playwright-community/playwright-go"
)

const browserViewLease = 15 * time.Second

type browserViewState struct {
	Open       bool   `json:"open"`
	Instance   string `json:"instance"`
	Controlled bool   `json:"controlled"`
}
type browserViewFrame struct {
	Instance       string `json:"instance"`
	Sequence       uint64 `json:"sequence"`
	Timestamp      int64  `json:"timestamp"`
	JPEG           string `json:"jpeg"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	ViewportWidth  int    `json:"viewportWidth"`
	ViewportHeight int    `json:"viewportHeight"`
	ContentX       int    `json:"contentX"`
	ContentY       int    `json:"contentY"`
	ContentWidth   int    `json:"contentWidth"`
	ContentHeight  int    `json:"contentHeight"`
}
type browserViewer struct {
	expires     time.Time
	lastFrame   uint64
	awaitingAck bool
	sentAt      time.Time
	recent      map[uint64]time.Time
}

type browserLive struct {
	mu             sync.Mutex
	instance       string
	page           pw.Page
	stop           chan struct{}
	viewers        map[*subscriber]browserViewer
	owner          *subscriber
	token          string
	controlExpires time.Time
	sequence       uint64
	lastFrameAt    time.Time
	notify         func(browserViewState)
}

func (live *browserLive) stateLocked() browserViewState {
	return browserViewState{Open: live.page != nil, Instance: live.instance, Controlled: live.owner != nil && time.Now().Before(live.controlExpires)}
}
func (live *browserLive) state() browserViewState {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.stateLocked()
}
func (live *browserLive) announce() {
	live.mu.Lock()
	state, notify := live.stateLocked(), live.notify
	live.mu.Unlock()
	if notify != nil {
		notify(state)
	}
}

func (session *browserSession) startLive(page pw.Page, browserContext pw.BrowserContext) {
	live := &session.live
	live.mu.Lock()
	live.instance = uuid.NewString()
	instance := live.instance
	stop := make(chan struct{})
	live.stop, live.page = stop, page
	live.viewers = make(map[*subscriber]browserViewer)
	live.sequence = 0
	live.owner, live.token = nil, ""
	live.mu.Unlock()
	page.OnClose(func(pw.Page) { session.stopLive(instance) })
	// State notifications must not run while the action lock is held: runtime
	// workspace changes also own the runtime lock.
	go live.announce()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var cdp pw.CDPSession
		defer func() {
			if cdp != nil {
				_ = cdp.Detach()
			}
		}()
		var lastCaptureError time.Time
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			live.mu.Lock()
			if live.instance != instance || live.page == nil {
				live.mu.Unlock()
				return
			}
			changed := live.pruneLocked(time.Now())
			watching := len(live.viewers) > 0
			live.mu.Unlock()
			if changed {
				live.announce()
			}
			if !watching {
				continue
			}
			// Reset after work so a slow capture cannot accumulate ticker ticks
			// and produce a burst of frames faster than the 2 FPS cap.
			ticker.Reset(500 * time.Millisecond)
			if cdp == nil {
				var err error
				cdp, err = browserContext.NewCDPSession(page)
				if err != nil {
					continue
				}
			}
			// Independent CDP capture: do not take the browser action mutex and
			// do not call snapshot(), which changes the agent's element refs.
			frame, err := captureBrowserView(cdp, instance)
			ticker.Reset(500 * time.Millisecond)
			if err != nil {
				if time.Since(lastCaptureError) > 10*time.Second {
					log.Printf("browser live capture failed: %v", err)
					lastCaptureError = time.Now()
				}
				continue
			}
			live.mu.Lock()
			if live.instance != instance || live.page == nil {
				live.mu.Unlock()
				return
			}
			live.sequence++
			frame.Sequence = live.sequence
			live.lastFrameAt = time.Now()
			for viewer, record := range live.viewers {
				// Wait for decoded/displayed acknowledgement. At most one frame
				// can be in the socket/crypto/decoder pipeline per viewer.
				if record.awaitingAck && time.Since(record.sentAt) < 5*time.Second {
					continue
				}
				record.lastFrame = frame.Sequence
				record.awaitingAck = true
				record.sentAt = time.Now()
				if record.recent == nil {
					record.recent = make(map[uint64]time.Time)
				}
				for id, at := range record.recent {
					if time.Since(at) > 15*time.Second {
						delete(record.recent, id)
					}
				}
				record.recent[frame.Sequence] = record.sentAt
				live.viewers[viewer] = record
				viewer.enqueueFrame(backendResponse{Type: "browser_frame", BrowserFrame: frame})
			}
			live.mu.Unlock()
		}
	}()
}

func captureBrowserView(cdp pw.CDPSession, instance string) (*browserViewFrame, error) {
	// The page remains 1280x800. Scale its full viewport to 1152x720 and
	// letterbox into a 1280x720 stream rather than changing responsive layout.
	value, err := cdp.Send("Page.captureScreenshot", map[string]any{
		"format": "jpeg", "quality": 75, "captureBeyondViewport": false,
	})
	if err != nil {
		return nil, err
	}
	object, _ := value.(map[string]any)
	encoded, _ := object["data"].(string)
	if encoded == "" || len(encoded) > 4<<20 {
		return nil, errors.New("invalid live frame size")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 {
		return nil, errors.New("invalid live frame dimensions")
	}
	source, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, errors.New("invalid viewport dimensions")
	}
	scale := math.Min(1280/float64(w), 720/float64(h))
	cw, ch := int(float64(w)*scale), int(float64(h)*scale)
	cx, cy := (1280-cw)/2, (720-ch)/2
	canvas := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(canvas, canvas.Bounds(), image.Black, image.Point{}, draw.Src)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			canvas.Set(cx+x, cy+y, source.At(bounds.Min.X+x*w/cw, bounds.Min.Y+y*h/ch))
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, canvas, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	if output.Len() > 1<<20 {
		return nil, errors.New("live JPEG exceeds 1 MiB")
	}
	return &browserViewFrame{Instance: instance, Timestamp: time.Now().UnixMilli(), JPEG: base64.StdEncoding.EncodeToString(output.Bytes()), Width: 1280, Height: 720,
		ViewportWidth: w, ViewportHeight: h, ContentX: cx, ContentY: cy, ContentWidth: cw, ContentHeight: ch}, nil
}

func (session *browserSession) stopLive(instance string) {
	live := &session.live
	live.mu.Lock()
	if instance != "" && live.instance != instance {
		live.mu.Unlock()
		return
	}
	if live.stop != nil {
		close(live.stop)
		live.stop = nil
	}
	for viewer := range live.viewers {
		viewer.clearFrame()
	}
	live.page, live.owner, live.token = nil, nil, ""
	live.viewers = nil
	live.mu.Unlock()
	go live.announce()
}

func (live *browserLive) pruneLocked(now time.Time) bool {
	for viewer, record := range live.viewers {
		closed := false
		select {
		case <-viewer.done:
			closed = true
		default:
		}
		if closed || now.After(record.expires) {
			delete(live.viewers, viewer)
			viewer.clearFrame()
		}
	}
	_, watching := live.viewers[live.owner]
	if live.owner != nil && (!watching || now.After(live.controlExpires)) {
		live.owner, live.token = nil, ""
		return true
	}
	return false
}
func (live *browserLive) watch(viewer *subscriber, instance string) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.page == nil || instance != live.instance {
		return errors.New("browser instance is no longer open")
	}
	record := live.viewers[viewer]
	record.expires = time.Now().Add(browserViewLease)
	live.viewers[viewer] = record
	return nil
}
func (live *browserLive) acknowledge(viewer *subscriber, command backendCommand) {
	live.mu.Lock()
	defer live.mu.Unlock()
	record, ok := live.viewers[viewer]
	if ok && command.BrowserInstance == live.instance && command.FrameID == record.lastFrame {
		record.awaitingAck = false
		live.viewers[viewer] = record
	}
}

func (live *browserLive) unwatch(viewer *subscriber) {
	live.mu.Lock()
	delete(live.viewers, viewer)
	viewer.clearFrame()
	changed := live.owner == viewer
	if changed {
		live.owner, live.token = nil, ""
	}
	live.mu.Unlock()
	if changed {
		live.announce()
	}
}
func (live *browserLive) manuallyControlled() bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.pruneLocked(time.Now())
	return live.owner != nil
}

func (session *browserSession) control(viewer *subscriber, command backendCommand) (string, error) {
	// Never block the protocol reader behind a 10-second agent wait. Clients
	// can retry once the current action completes.
	if !session.mu.TryLock() {
		return "", errors.New("browser action in progress; retry Take control when it finishes")
	}
	defer session.mu.Unlock()
	live := &session.live
	live.mu.Lock()
	live.pruneLocked(time.Now())
	_, watching := live.viewers[viewer]
	if live.page == nil || command.BrowserInstance != live.instance || !watching {
		live.mu.Unlock()
		return "", errors.New("subscribe to the current browser before taking control")
	}
	if live.owner != nil && live.owner != viewer {
		live.mu.Unlock()
		return "", errors.New("another viewer controls this browser")
	}
	if live.owner == nil {
		live.token = uuid.NewString()
	}
	live.owner = viewer
	live.controlExpires = time.Now().Add(browserViewLease)
	token := live.token
	live.mu.Unlock()
	session.cursorPlaced = false
	session.clearRefs()
	go live.announce()
	return token, nil
}
func (live *browserLive) release(viewer *subscriber) {
	live.mu.Lock()
	changed := live.owner == viewer
	if changed {
		live.owner, live.token = nil, ""
	}
	live.mu.Unlock()
	if changed {
		live.announce()
	}
}

func (session *browserSession) manualInput(viewer *subscriber, command backendCommand) error {
	if !session.mu.TryLock() {
		return errors.New("browser busy; retry input")
	}
	defer session.mu.Unlock()
	live := &session.live
	live.mu.Lock()
	live.pruneLocked(time.Now())
	record, watching := live.viewers[viewer]
	valid := live.page != nil && command.BrowserInstance == live.instance && live.owner == viewer && command.ControlToken != "" && command.ControlToken == live.token && watching
	if !valid {
		live.mu.Unlock()
		return errors.New("manual control lease expired or browser changed")
	}
	frameSentAt, frameKnown := record.recent[command.FrameID]
	if command.FrameID == 0 || !frameKnown || time.Since(frameSentAt) > 10*time.Second {
		log.Printf("browser input rejected: frame=%d latestSent=%d known=%t age=%s", command.FrameID, record.lastFrame, frameKnown, time.Since(frameSentAt).Round(time.Millisecond))
		live.mu.Unlock()
		return fmt.Errorf("stale browser frame %d (latest sent %d); wait for a current image", command.FrameID, record.lastFrame)
	}
	live.controlExpires = time.Now().Add(browserViewLease)
	live.mu.Unlock()
	session.cursorPlaced = false
	session.clearRefs()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Bound protocol stalls even when a page runs unresponsive JavaScript.
	finished, joined := make(chan struct{}), make(chan struct{})
	browserContext := session.context
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = browserContext.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-joined }()
	switch command.BrowserAction {
	case "click":
		x, err := browserCoordinate(command.X, 1280)
		if err != nil {
			return err
		}
		y, err := browserCoordinate(command.Y, 800)
		if err != nil {
			return err
		}
		button := pw.MouseButtonLeft
		if command.Button == "right" {
			button = pw.MouseButtonRight
		} else if command.Button != "left" {
			return errors.New("button must be left or right")
		}
		return session.page.Mouse().Click(x, y, pw.MouseClickOptions{Button: button})
	case "type":
		value, err := browserKeyboardArgument(map[string]any{"text": command.Text}, "type")
		if err != nil {
			return err
		}
		// Human entry uses Unicode insertText, not per-character remote calls.
		return session.page.Keyboard().InsertText(value)
	case "key":
		return session.sendKeyboard("press_key", map[string]any{"key": command.Key})
	case "scroll":
		delta := 720.0
		if command.Direction == "up" {
			delta = -delta
		} else if command.Direction != "down" {
			return errors.New("direction must be up or down")
		}
		return session.page.Mouse().Wheel(0, delta)
	default:
		return fmt.Errorf("unsupported browser input action")
	}
}
