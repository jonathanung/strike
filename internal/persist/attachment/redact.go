package attachment

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// RedactName scrubs credential-shaped spans from an attachment display name.
func RedactName(name string) string {
	return redact.String(name)
}

// RedactRegions paints opaque black rectangles over PNG or JPEG pixels and
// re-encodes as PNG (stripping EXIF and other ancillary metadata). Other
// formats fail visibly.
func RedactRegions(raw []byte, mime string, regions []Region) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrEmpty
	}
	if len(regions) == 0 {
		return nil, fmt.Errorf("%w: no regions", ErrUnsupported)
	}
	mime = normalizeMIME(mime)
	if mime == "" {
		mime = SniffMIME(raw)
	}
	img, err := decodeRaster(raw, mime)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	black := image.NewUniform(color.RGBA{A: 255})
	b := dst.Bounds()
	for _, r := range regions {
		if r.W <= 0 || r.H <= 0 {
			return nil, fmt.Errorf("%w: invalid region %+v", ErrUnsupported, r)
		}
		rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H).Intersect(b)
		if rect.Empty() {
			return nil, fmt.Errorf("%w: region %+v outside image", ErrUnsupported, r)
		}
		draw.Draw(dst, rect, black, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("attachment: encode redacted png: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeRaster(raw []byte, mime string) (image.Image, error) {
	switch mime {
	case "image/png":
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: png decode: %v", ErrUnsupported, err)
		}
		return img, nil
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: jpeg decode: %v", ErrUnsupported, err)
		}
		return img, nil
	default:
		kind := KindFromMIME(mime)
		if kind == "" {
			kind = mime
		}
		return nil, fmt.Errorf("%w: region redaction requires png/jpeg (got %s)", ErrUnsupported, strings.TrimSpace(kind))
	}
}
