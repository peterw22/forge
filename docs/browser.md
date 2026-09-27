# Browser tools and live view

The agent can drive a headless browser, and Forge can show that browser live and take control of it.

The browser is not a sandbox. Read [Safety](#safety) before you point it at anything that matters.

## Setup

Install the driver and Chromium on the agent's machine:

```bash
bash scripts/install-browser.sh
FORGE_BROWSER_TEST=1 go test ./cmd/pi-go-agent -run '^TestBrowserHeadlessIntegration$' -v
```

Chromium runs headless; no display is needed. Each session has one temporary context and page, and no personal browser profile is used. Browser state survives between turns and is discarded when the browser is closed, the workspace changes, a browser operation is cancelled, or the agent stops.

The browser is installed in the user's Playwright cache. It is not part of the agent binary or of any packaged app.

## Tools

| Tool | Does |
|---|---|
| `browser_navigate` | Opens a URL |
| `browser_click` | Clicks an element by its reference |
| `browser_type` | Types text into the focused field |
| `browser_press_key` | Sends one key or chord |
| `browser_scroll` | Scrolls the page up or down |
| `browser_place_cursor` | Moves the pointer to a position |
| `browser_click_cursor` | Clicks where the pointer is |
| `browser_screenshot` | Captures the viewport now |
| `browser_close` | Closes the browser |

### Results

A result is a PNG of the viewport together with references to the visible elements, such as `e12`. References are replaced by every snapshot. A reference to an element that is gone fails; it never resolves to another element.

Screenshots reach the model as images and appear in the transcript. A model without vision cannot interpret them.

### Waiting

Navigation and clicks wait until the page has made no HTTP request for 500 ms, for at most 10 seconds, and return a screenshot either way. WebSockets are not counted. Navigation has its own 10-second limit for the response to begin.

`browser_screenshot` does not wait.

### Scrolling

`browser_scroll` moves the page by 90% of the viewport and reports when it cannot move further. Scrolling inside a nested panel is not supported.

### The pointer

`browser_place_cursor` takes a position in the 1280×800 viewport, moves the pointer without clicking, and waits 100 ms for hover effects. The returned image has a red crosshair drawn on it, because a page screenshot does not include the pointer. The page itself is not changed.

`browser_click_cursor` clicks with the left or right button. Place the pointer again after navigating, scrolling or clicking.

A position targets a point, not an element. If the page changes between placing and clicking, the click may land on something else.

### Typing

`browser_type` types up to 10,000 bytes into the focused field; click the field first. To replace text, select everything with `ControlOrMeta+A`, then type.

Typed text is recorded in the transcript. Do not type secrets.

## Safety

Only `browser_navigate` passes the [safety gate](security/safety-gate.md). Opening a page permits what follows on it: clicks, typing, redirects, background requests and screenshots are not checked individually.

- Page text and screenshots are sent to the model, including anything sensitive on the page.
- Pressing Enter or clicking a button can submit a form or change data on a remote site.
- Only `http` and `https` URLs are accepted, and none with credentials in them.
- Downloads are not saved, there is no upload tool, and extra tabs are not supported.
- A pop-up is closed, though its first request may already have been made.
- The model cannot run JavaScript in the page.

Use sites you trust.

## Live view

When a session has an open browser, Forge shows a browser button beside the transcript.

| Window width | Layout |
|---|---|
| 1,100 points or more | Chat and browser side by side, with a divider you can drag |
| Narrower | The browser covers the chat. Back or Minimize turns it into a small preview you can drag |

Resizing the window switches between the two without restarting the stream or losing the zoom. The preview is inside Forge; it does not float over other apps. Putting Forge in the background stops the stream.

### The stream

Frames are 1280×720 JPEG images, at most two per second, sent over the same authenticated, encrypted connection as everything else. They are not sent to the model and are not stored in the session.

Pinch to zoom and drag to pan. The arrow buttons scroll the page.

### Taking control

The view starts read-only. **Take control** gives one client the controls once the agent's current browser action has finished. While a person has control, the agent's browser tools are blocked.

| Gesture | Effect |
|---|---|
| Tap | Left click |
| Long press, or secondary click | Right click |
| Text field and Send | Types into the focused field |
| Key buttons | Enter, Tab, Backspace, Escape, arrows, select all |

Manual input is not added to the conversation. The page still receives it, and it can appear in later screenshots.

Several clients can watch; one can control. Control is renewed every five seconds and lapses after fifteen. It is released when the view is closed or minimized, Forge goes to the background, the connection drops, the session changes or the browser closes.

After a person has had control, the agent's element references and pointer position are no longer valid. It needs a fresh screenshot.

### Slow connections

The agent keeps at most one frame in flight for each viewer and replaces a waiting frame with a newer one. A slow client therefore sees fewer frames instead of falling behind.

Input refers to the frame the viewer was shown. Input against a frame that is more than ten seconds old, a lapsed control, or an earlier browser is rejected.
