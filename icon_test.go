// Modified for Claude Swap Command (2026).
package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestTrayIconICO(t *testing.T) {
	ico := encodeICO(drawIcon(50, 96))
	if want := 6 + 16 + 40 + 32*32*4 + 4*32; len(ico) != want {
		t.Fatalf("ico is %d bytes, want %d", len(ico), want)
	}
}

// Set ICON_OUT to a folder to look at the icons.
func TestTrayIconPreview(t *testing.T) {
	out := os.Getenv("ICON_OUT")
	if out == "" {
		t.Skip("ICON_OUT not set")
	}
	for _, pct := range []float64{-1, 24, 80, 97} {
		img := drawIcon(pct, 96)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(out, "icon-"+strconv.Itoa(int(pct))+".png"), buf.Bytes(), 0o644)
	}
}
