// Command ziggygen makes the Ziggy frames the TUI embeds: the eyes-open and
// blink images, cropped to the same box so the blink lines up, and scaled
// down to a size that's cheap to embed and send to the terminal. Builds
// only include them with the `brand` tag, because Ziggy may only ship after
// brand approval (PLAN.md section 14, item 1).
//
//	go run ./tools/ziggygen -in ~/Source/temporal-toolkit-demo-buildkit/assets/brand/ziggy-peek.png \
//	  -blink ~/Source/temporal-toolkit-demo-buildkit/assets/brand/ziggy-blink.png
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

func main() {
	in := flag.String("in", "", "eyes-open PNG")
	blink := flag.String("blink", "", "eyes-closed PNG on the same canvas")
	out := flag.String("out", "internal/tui/brand", "output directory")
	width := flag.Int("width", 256, "output width in pixels")
	flag.Parse()

	open, closed := load(*in), load(*blink)
	if open.Bounds() != closed.Bounds() {
		log.Fatal("the two frames must share a canvas")
	}
	box := opaqueBounds(open).Union(opaqueBounds(closed))
	// A little padding keeps Ziggy's outline off the edge.
	pad := box.Dx() / 40
	box = image.Rect(box.Min.X-pad, box.Min.Y-pad, box.Max.X+pad, box.Max.Y+pad).Intersect(open.Bounds())

	h := *width * box.Dy() / box.Dx()
	for name, img := range map[string]image.Image{"ziggy-open.png": open, "ziggy-blink.png": closed} {
		dst := image.NewNRGBA(image.Rect(0, 0, *width, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, box, draw.Src, nil)
		path := filepath.Join(*out, name)
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(f, dst); err != nil {
			log.Fatal(err)
		}
		f.Close()
		fmt.Printf("wrote %s (%dx%d)\n", path, *width, h)
	}
}

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	return img
}

func opaqueBounds(img image.Image) image.Rectangle {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x0fff {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x+1), max(maxY, y+1)
			}
		}
	}
	if minX >= maxX {
		return b
	}
	return image.Rect(minX, minY, maxX, maxY)
}
