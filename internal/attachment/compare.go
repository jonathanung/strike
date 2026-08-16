package attachment

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// Compare produces structured before/after evidence. Decodable same-size
// PNG/JPEG pairs use a pixel count; otherwise equality is content-hash only.
func Compare(a, b []byte) CompareResult {
	out := CompareResult{
		SHA256A: SumHex(a),
		SHA256B: SumHex(b),
		Method:  "sha256",
	}
	if out.SHA256A == out.SHA256B && len(a) > 0 {
		out.Equal = true
		if img, err := decodeAnyRaster(a); err == nil {
			r := img.Bounds()
			out.Width = r.Dx()
			out.Height = r.Dy()
			out.Method = "sha256+bounds"
		}
		return out
	}
	ia, errA := decodeAnyRaster(a)
	ib, errB := decodeAnyRaster(b)
	if errA != nil || errB != nil {
		out.Equal = false
		out.Detail = "hash mismatch; pixel compare unavailable"
		if errA != nil {
			out.Detail += ": a: " + errA.Error()
		}
		if errB != nil {
			out.Detail += ": b: " + errB.Error()
		}
		return out
	}
	ra, rb := ia.Bounds(), ib.Bounds()
	out.Width = ra.Dx()
	out.Height = ra.Dy()
	if ra.Dx() != rb.Dx() || ra.Dy() != rb.Dy() {
		out.Method = "pixel"
		out.Equal = false
		out.Detail = fmt.Sprintf("dimension mismatch %dx%d vs %dx%d", ra.Dx(), ra.Dy(), rb.Dx(), rb.Dy())
		return out
	}
	diff := 0
	for y := ra.Min.Y; y < ra.Max.Y; y++ {
		for x := ra.Min.X; x < ra.Max.X; x++ {
			if !sameRGBA(ia.At(x, y), ib.At(x, y)) {
				diff++
			}
		}
	}
	out.Method = "pixel"
	out.DiffPixels = diff
	out.Equal = diff == 0
	if !out.Equal {
		out.Detail = fmt.Sprintf("%d differing pixels", diff)
	}
	return out
}

func sameRGBA(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func decodeAnyRaster(raw []byte) (image.Image, error) {
	mime := SniffMIME(raw)
	switch mime {
	case "image/png":
		return png.Decode(bytes.NewReader(raw))
	case "image/jpeg":
		return jpeg.Decode(bytes.NewReader(raw))
	default:
		return nil, fmt.Errorf("not a png/jpeg raster")
	}
}
