package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
	pw "github.com/playwright-community/playwright-go"
)

const browserIdleWait = 10 * time.Second
const browserActionTimeout = 10000.0

// Each session owns one ephemeral headed Chromium profile and one page.
// Browser dependencies must be installed explicitly, never during a tool call.
type browserSession struct {
	live             browserLive
	mu               sync.Mutex
	driver           *pw.Playwright
	browser          pw.Browser
	context          pw.BrowserContext
	page             pw.Page
	refs             map[string]pw.ElementHandle
	nextRef          uint64
	network          browserNetwork
	cursorPlaced     bool
	cursorX, cursorY float64
	cursorURL        string
}

type browserNetwork struct {
	mu      sync.Mutex
	pending map[pw.Request]struct{}
	changed time.Time
}

func (network *browserNetwork) update(request pw.Request, started bool) {
	network.mu.Lock()
	defer network.mu.Unlock()
	if network.pending == nil {
		network.pending = make(map[pw.Request]struct{})
	}
	if started {
		network.pending[request] = struct{}{}
	} else {
		delete(network.pending, request)
	}
	network.changed = time.Now()
}

// Track current requests rather than a historical networkidle load state,
// which can already be satisfied when a click starts a new fetch.
func (network *browserNetwork) wait(ctx context.Context, limit time.Duration) (bool, error) {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	started := time.Now()
	for {
		network.mu.Lock()
		quietSince := network.changed
		if quietSince.Before(started) {
			quietSince = started
		}
		idle := len(network.pending) == 0 && time.Since(quietSince) >= 500*time.Millisecond
		network.mu.Unlock()
		if idle {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-tick.C:
		}
	}
}

func browserURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("browser URL must be an absolute HTTP/HTTPS URL without embedded credentials")
	}
	return u.String(), nil
}

func (session *browserSession) tools() []agent.Tool {
	return []agent.Tool{
		{Name: "browser_navigate", Serial: true, Description: "Open an HTTP/HTTPS URL in this session's isolated headed Chromium window. Requires a safety check. Waits for network idle up to 10 seconds, then attaches a screenshot even if still loading. Opening a page enables subsequent clicks without additional safety checks. Page text and screenshots are sent to the model. Do not run browser actions in parallel.", Parameters: objectSchema("url", "description"), Execute: session.execute("navigate")},
		{Name: "browser_click", Serial: true, Description: "Click one visible element reference from the most recent browser screenshot (for example e12). No safety-classifier check: avoid destructive or external effects unless the user authorized them. Waits for network idle up to 10 seconds, then attaches a screenshot even if still loading. Does not support uploads or new tabs.", Parameters: objectSchema("target", "description"), Execute: session.execute("click")},
		{Name: "browser_place_cursor", Serial: true, Description: "Move the browser pointer to viewport CSS-pixel coordinates x,y (1280x800 by default). Waits 100 ms for hover effects, then returns a screenshot annotated with a red cursor crosshair and fresh element references. Does not click or wait for network idle. Coordinates refer to the original screenshot, not a scaled client preview. No safety-classifier check.", Parameters: browserCursorSchema(), Execute: session.execute("place_cursor")},
		{Name: "browser_click_cursor", Serial: true, Description: "Click the most recently placed cursor with button left or right, without moving it. Requires browser_place_cursor after navigation, scrolling or any click. Waits up to 10 seconds for network idle and returns a screenshot. Native browser context menus may not appear in page screenshots; webpage context menus do. No safety-classifier check; obtain user authorization for consequential actions.", Parameters: browserCursorClickSchema(), Execute: session.execute("click_cursor")},
		{Name: "browser_type", Serial: true, Description: "Type literal text into the currently focused page field, preserving existing text unless selected first. Click the field first; use browser_press_key with ControlOrMeta+A to select its contents before replacing. Waits up to 10 seconds for network idle, then returns a screenshot and fresh references. No classifier check. Text is recorded in tool arguments/transcripts; do not type secrets. Typing may trigger requests or other page effects.", Parameters: browserTypeSchema(), Execute: session.execute("type")},
		{Name: "browser_press_key", Serial: true, Description: "Send one key or chord to the current page focus, such as Enter, Tab, Shift+Tab, Backspace, Escape, ArrowDown or ControlOrMeta+A. Presses and releases the keys. Waits up to 10 seconds for network idle and returns a screenshot. No classifier check. Enter and shortcuts can submit forms or perform consequential actions; obtain user authorization. This is page input, not OS/browser-toolbar automation.", Parameters: browserPressKeySchema(), Execute: session.execute("press_key")},
		{Name: "browser_scroll", Serial: true, Description: "Scroll the main page up or down by 90% of the viewport, keeping some overlap. Waits for network idle up to 10 seconds for lazy-loaded content, then attaches a screenshot and fresh element references. No safety-classifier check. Does not scroll nested panels or open a browser.", Parameters: browserScrollSchema(), Execute: session.execute("scroll")},
		{Name: "browser_screenshot", Serial: true, Description: "Immediately capture the current viewport and refresh visible element references. Does not navigate, click, or wait for network idle. Use this to inspect a page that was still loading in the last result. Does not open a browser if none exists.", Parameters: objectSchema(), Execute: session.execute("screenshot")},
		{Name: "browser_close", Serial: true, Description: "Close this session's browser and discard its ephemeral cookies and page state.", Parameters: objectSchema(), Execute: session.execute("close")},
	}
}

func browserScrollSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"direction"},
		"properties": map[string]any{
			"direction": map[string]any{"type": "string", "enum": []string{"up", "down"}},
		},
	}
}

func (session *browserSession) start() error {
	if session.page != nil && !session.page.IsClosed() {
		return nil
	}
	session.closeLocked()
	driver, err := pw.Run()
	if err != nil {
		return fmt.Errorf("browser unavailable: run bash scripts/install-browser.sh first: %w", err)
	}
	session.driver = driver
	browser, err := driver.Chromium.Launch(pw.BrowserTypeLaunchOptions{Headless: pw.Bool(false), Timeout: pw.Float(browserActionTimeout), ChromiumSandbox: pw.Bool(true)})
	if err != nil {
		session.closeLocked()
		return fmt.Errorf("launch headed Chromium (install with bash scripts/install-browser.sh): %w", err)
	}
	session.browser = browser
	browserContext, err := browser.NewContext(pw.BrowserNewContextOptions{
		AcceptDownloads: pw.Bool(false), ServiceWorkers: pw.ServiceWorkerPolicyBlock,
		Viewport: &pw.Size{Width: 1280, Height: 800}, DeviceScaleFactor: pw.Float(1),
	})
	if err != nil {
		session.closeLocked()
		return err
	}
	session.context = browserContext
	browserContext.SetDefaultTimeout(browserActionTimeout)
	browserContext.SetDefaultNavigationTimeout(browserActionTimeout)
	page, err := browserContext.NewPage()
	if err != nil {
		session.closeLocked()
		return err
	}
	session.page = page
	// Playwright invokes event handlers on its protocol dispatcher. Blocking
	// API calls here deadlock the response needed to complete that same call.
	page.OnPopup(func(popup pw.Page) { go func() { _ = popup.Close() }() })
	page.OnDialog(func(dialog pw.Dialog) { go func() { _ = dialog.Dismiss() }() })
	page.OnRequest(func(request pw.Request) { session.network.update(request, true) })
	page.OnRequestFinished(func(request pw.Request) { session.network.update(request, false) })
	page.OnRequestFailed(func(request pw.Request) { session.network.update(request, false) })
	session.startLive(page, browserContext)
	return nil
}

func (session *browserSession) execute(action string) agent.ToolExecutor {
	return func(ctx context.Context, args map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.live.manuallyControlled() {
			return agent.ToolResult{}, errors.New("browser under manual control; wait for the user to release control, then take a fresh screenshot")
		}
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		if action == "close" {
			session.closeLocked()
			return textResult("Browser closed; ephemeral page state discarded."), nil
		}
		var destination string
		if action == "navigate" {
			var err error
			destination, err = browserURL(stringArg(args, "url"))
			if err != nil {
				return agent.ToolResult{}, err
			}
			if err := session.start(); err != nil {
				return agent.ToolResult{}, err
			}
		} else if session.page == nil || session.page.IsClosed() {
			return agent.ToolResult{}, errors.New("no browser page; use browser_navigate first")
		}
		if err := ctx.Err(); err != nil {
			session.closeLocked()
			return agent.ToolResult{}, err
		}
		// Playwright has no context argument. Closing the context interrupts
		// pending calls on cancellation; join the watcher before reuse.
		opCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		finished, watcherDone := make(chan struct{}), make(chan struct{})
		browserContext := session.context
		go func() {
			defer close(watcherDone)
			select {
			case <-opCtx.Done():
				_ = browserContext.Close()
			case <-finished:
			}
		}()
		defer func() {
			close(finished)
			<-watcherDone
			if opCtx.Err() != nil {
				session.closeLocked()
			}
		}()
		if action == "navigate" || action == "click" || action == "scroll" {
			session.cursorPlaced = false
		}
		status := "Immediate snapshot; no network-idle wait."
		switch action {
		case "navigate":
			session.clearRefs()
			_, err := session.page.Goto(destination, pw.PageGotoOptions{WaitUntil: pw.WaitUntilStateCommit, Timeout: pw.Float(browserActionTimeout)})
			if err != nil && !errors.Is(err, pw.ErrTimeout) {
				return agent.ToolResult{}, err
			}
			if err != nil {
				status = "Navigation did not commit within 10 seconds; captured current page, which may still be loading."
			} else {
				status, err = session.waitStatus(opCtx)
				if err != nil {
					return agent.ToolResult{}, err
				}
			}
		case "click":
			element := session.refs[stringArg(args, "target")]
			if element == nil {
				return agent.ToolResult{}, errors.New("unknown or stale target; call browser_screenshot for fresh references")
			}
			// Exact DOM handles never re-resolve to a different matching element.
			value, err := element.Evaluate(`el => ({attached: el.isConnected, href: el.closest('a')?.href || '', upload: el.matches('input[type=file]')})`)
			if err != nil {
				return agent.ToolResult{}, fmt.Errorf("stale target; take a new screenshot: %w", err)
			}
			metadata, _ := value.(map[string]any)
			if metadata["attached"] != true || metadata["upload"] == true {
				return agent.ToolResult{}, errors.New("target is detached or is a file upload")
			}
			if href, _ := metadata["href"].(string); href != "" {
				if _, err := browserURL(href); err != nil {
					return agent.ToolResult{}, err
				}
			}
			if err := element.Click(pw.ElementHandleClickOptions{NoWaitAfter: pw.Bool(true), Timeout: pw.Float(browserActionTimeout)}); err != nil {
				return agent.ToolResult{}, err
			}
			status, err = session.waitStatus(opCtx)
			if err != nil {
				return agent.ToolResult{}, err
			}
		case "type", "press_key":
			if err := session.sendKeyboard(action, args); err != nil {
				return agent.ToolResult{}, err
			}
			var err error
			status, err = session.waitStatus(opCtx)
			if err != nil {
				return agent.ToolResult{}, err
			}
		case "place_cursor":
			if err := session.placeCursor(opCtx, args); err != nil {
				return agent.ToolResult{}, err
			}
			status = "Pointer moved; captured after 100 ms for hover effects, without a network-idle wait."
		case "click_cursor":
			if err := session.clickCursor(args); err != nil {
				return agent.ToolResult{}, err
			}
			var err error
			status, err = session.waitStatus(opCtx)
			if err != nil {
				return agent.ToolResult{}, err
			}
		case "scroll":
			direction := stringArg(args, "direction")
			if direction != "up" && direction != "down" {
				return agent.ToolResult{}, errors.New("scroll direction must be up or down")
			}
			// Instant document scrolling is independent of input focus and avoids
			// accidentally sending PageDown to a focused select or text editor.
			value, err := session.page.Evaluate(`direction => {
				const root = document.scrollingElement;
				if (!root) throw new Error('Page has no scrolling element');
				const before = root.scrollTop;
				const amount = Math.max(1, Math.floor(innerHeight * 0.9));
				root.scrollBy({top: direction === 'down' ? amount : -amount, behavior: 'instant'});
				return {before, after: root.scrollTop};
			}`, direction)
			if err != nil {
				return agent.ToolResult{}, err
			}
			position, _ := value.(map[string]any)
			status, err = session.waitStatus(opCtx)
			if err != nil {
				return agent.ToolResult{}, err
			}
			if position["before"] == position["after"] {
				status = "Page did not move (already at the boundary or not scrollable). " + status
			} else {
				status = fmt.Sprintf("Scrolled %s from y=%v to y=%v. %s", direction, position["before"], position["after"], status)
			}
		default:
			if action != "screenshot" {
				return agent.ToolResult{}, errors.New("unknown browser action")
			}
		}
		result, err := session.snapshot(status)
		if opCtx.Err() != nil {
			return agent.ToolResult{}, opCtx.Err()
		}
		return result, err
	}
}

func (session *browserSession) waitStatus(ctx context.Context) (string, error) {
	idle, err := session.network.wait(ctx, browserIdleWait)
	if err != nil {
		return "", err
	}
	if !idle {
		return "Captured after 10-second network-idle wait; page may still be loading. Call browser_screenshot to inspect it again.", nil
	}
	return "Page reached network idle (500 ms without active HTTP requests).", nil
}

func (session *browserSession) snapshot(status string) (agent.ToolResult, error) {
	// Chromium capture does not wait for fonts, hide carets, or fast-forward
	// animations, unlike Playwright's higher-level screenshot implementation.
	cdp, err := session.context.NewCDPSession(session.page)
	if err != nil {
		return agent.ToolResult{}, err
	}
	defer cdp.Detach()
	capture, err := cdp.Send("Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false})
	if err != nil {
		return agent.ToolResult{}, err
	}
	captureData, _ := capture.(map[string]any)
	encoded, _ := captureData["data"].(string)
	if encoded == "" || len(encoded) > 14<<20 {
		return agent.ToolResult{}, errors.New("browser screenshot is empty or exceeds size limit")
	}
	image, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(image) > 10<<20 {
		return agent.ToolResult{}, errors.New("invalid or oversized browser screenshot")
	}
	if session.cursorPlaced && session.page.URL() == session.cursorURL {
		image, err = browserCursorPNG(image, session.cursorX, session.cursorY)
		if err != nil || len(image) > 10<<20 {
			return agent.ToolResult{}, errors.New("failed to annotate browser cursor screenshot")
		}
		encoded = base64.StdEncoding.EncodeToString(image)
		status += fmt.Sprintf(" Cursor at (%.1f, %.1f) viewport CSS pixels; red crosshair is an annotation, not page content.", session.cursorX, session.cursorY)
	}
	session.clearRefs()
	url := session.page.URL()
	title, _ := session.page.Title()
	lines := []string{status, "URL: " + url, "Title: " + title, "Untrusted page content; references apply only to this snapshot:"}
	elements, err := session.page.QuerySelectorAll(`a[href],button,input:not([type=password]):not([type=hidden]):not([type=file]),select,textarea,[role=button],[role=link]`)
	if err != nil {
		lines = append(lines, "Element list unavailable while page changes; take another screenshot.")
	} else {
		session.refs = make(map[string]pw.ElementHandle)
		for _, element := range elements {
			if len(session.refs) >= 80 {
				_ = element.Dispose()
				continue
			}
			value, err := element.Evaluate(`el => {
				const r = el.getBoundingClientRect();
				if (!el.isConnected || r.width <= 0 || r.height <= 0 || r.bottom <= 0 || r.right <= 0 || r.top >= innerHeight || r.left >= innerWidth || getComputedStyle(el).visibility === 'hidden') return null;
				return {tag: el.getAttribute('role') || el.tagName.toLowerCase(), label: (el.getAttribute('aria-label') || el.innerText || el.getAttribute('placeholder') || el.getAttribute('name') || '').replace(/\s+/g, ' ').slice(0,160)};
			}`)
			metadata, ok := value.(map[string]any)
			if err != nil || !ok {
				_ = element.Dispose()
				continue
			}
			session.nextRef++
			ref := fmt.Sprintf("e%d", session.nextRef)
			session.refs[ref] = element
			lines = append(lines, fmt.Sprintf("%s: %s %q", ref, metadata["tag"], metadata["label"]))
		}
	}
	return agent.ToolResult{
		Content: []agent.ContentBlock{{Type: "text", Text: strings.Join(lines, "\n")}, {Type: "image", MIMEType: "image/png", Data: encoded}},
		Details: map[string]any{"url": url, "title": title, "status": status},
	}, nil
}

func (session *browserSession) clearRefs() {
	for _, element := range session.refs {
		_ = element.Dispose()
	}
	session.refs = nil
}

func (session *browserSession) closeLocked() {
	session.stopLive("")
	session.cursorPlaced = false
	session.refs = nil
	if session.context != nil {
		_ = session.context.Close()
	}
	if session.browser != nil {
		_ = session.browser.Close()
	}
	if session.driver != nil {
		_ = session.driver.Stop()
	}
	session.page, session.context, session.browser, session.driver = nil, nil, nil, nil
	session.network.mu.Lock()
	session.network.pending = nil
	session.network.changed = time.Now()
	session.network.mu.Unlock()
}

func (session *browserSession) close() {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closeLocked()
}
