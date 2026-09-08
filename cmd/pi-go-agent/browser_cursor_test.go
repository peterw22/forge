package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"math"
	"testing"
	"time"
)

func TestBrowserCursorCoordinates(t *testing.T) {
	for _, value := range []any{nil, "12", -1, 1280, math.NaN(), math.Inf(1)} {
		if _, err := browserCoordinate(value, 1280); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	for _, value := range []any{0, 1279, 45.5} {
		if _, err := browserCoordinate(value, 1280); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserHoverDelayAndCancellation(t *testing.T) {
	start := time.Now()
	if err := waitBrowserHover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("hover captured too early")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitBrowserHover(ctx); err == nil {
		t.Fatal("cancelled hover succeeded")
	}
}

func TestBrowserCursorMarker(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	for _, point := range [][2]float64{{0, 0}, {10, 10}, {19, 19}} {
		encoded, err := browserCursorPNG(data.Bytes(), point[0], point[1])
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		r, _, _, a := img.At(int(point[0]), int(point[1])).RGBA()
		if r == 0 || a == 0 {
			t.Fatal("cursor center not rendered")
		}
	}
}
