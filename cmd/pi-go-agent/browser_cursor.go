package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"time"

	pw "github.com/playwright-community/playwright-go"
)

const browserHoverDelay = 100 * time.Millisecond

func browserCursorSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"x", "y"},
		"properties": map[string]any{
			"x": map[string]any{"type": "number", "minimum": 0, "description": "Horizontal CSS pixel coordinate from the left of the browser viewport, not the phone's displayed preview."},
			"y": map[string]any{"type": "number", "minimum": 0, "description": "Vertical CSS pixel coordinate from the top of the browser viewport, not document coordinates."},
		},
	}
}

func browserCursorClickSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"button"},
		"properties": map[string]any{
			"button": map[string]any{"type": "string", "enum": []string{"left", "right"}},
		},
	}
}

func browserCoordinate(value any, limit int) (float64, error) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case int:
		number = float64(value)
	default:
		return 0, errors.New("cursor coordinates must be finite numbers inside the viewport")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number >= float64(limit) {
		return 0, errors.New("cursor coordinates must be finite numbers inside the viewport")
	}
	return number, nil
}

func waitBrowserHover(ctx context.Context) error {
	timer := time.NewTimer(browserHoverDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (session *browserSession) placeCursor(ctx context.Context, args map[string]any) error {
	size := session.page.ViewportSize()
	if size == nil {
		return errors.New("browser viewport size is unavailable")
	}
	x, err := browserCoordinate(args["x"], size.Width)
	if err != nil {
		return err
	}
	y, err := browserCoordinate(args["y"], size.Height)
	if err != nil {
		return err
	}
	session.cursorPlaced = false
	if err := session.page.Mouse().Move(x, y); err != nil {
		return err
	}
	session.cursorX, session.cursorY = x, y
	session.cursorURL = session.page.URL()
	session.cursorPlaced = true
	return waitBrowserHover(ctx)
}

func (session *browserSession) clickCursor(args map[string]any) error {
	var button *pw.MouseButton
	switch stringArg(args, "button") {
	case "left":
		button = pw.MouseButtonLeft
	case "right":
		button = pw.MouseButtonRight
	default:
		return errors.New("cursor button must be left or right")
	}
	if !session.cursorPlaced || session.page.URL() != session.cursorURL {
		return errors.New("place the cursor with browser_place_cursor before clicking; navigation, scrolling and clicks invalidate placement")
	}
	// Do not use Mouse.Click: it moves the pointer again. Send down/up at its
	// current position, always attempting release even if the press fails.
	session.cursorPlaced = false
	pressErr := session.page.Mouse().Down(pw.MouseDownOptions{Button: button})
	releaseErr := session.page.Mouse().Up(pw.MouseUpOptions{Button: button})
	return errors.Join(pressErr, releaseErr)
}

// Chromium screenshots omit the OS pointer. Annotate the returned PNG rather
// than injecting DOM that could affect hover, hit testing, or page behavior.
func browserCursorPNG(data []byte, x, y float64) ([]byte, error) {
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(source.Bounds())
	draw.Draw(canvas, canvas.Bounds(), source, source.Bounds().Min, draw.Src)
	cx, cy := int(math.Round(x)), int(math.Round(y))
	white, red := color.RGBA{255, 255, 255, 255}, color.RGBA{240, 30, 50, 255}
	for dy := -10; dy <= 10; dy++ {
		for dx := -10; dx <= 10; dx++ {
			distance := dx*dx + dy*dy
			if (distance >= 36 && distance <= 100) || (dx >= -2 && dx <= 2) || (dy >= -2 && dy <= 2) {
				canvas.SetRGBA(cx+dx, cy+dy, white)
			}
			if (distance >= 49 && distance <= 81) || dx == 0 || dy == 0 {
				canvas.SetRGBA(cx+dx, cy+dy, red)
			}
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
