package attachment

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestClassifyFormats(t *testing.T) {
	pngRaw := encodeSolidPNG(t, 2, 2, color.RGBA{R: 255, A: 255})
	cases := []struct {
		name string
		raw  []byte
		mime string
		kind string
		want string
		err  error
	}{
		{name: "png", raw: pngRaw, want: KindImage},
		{name: "pdf", raw: []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"), want: KindPDF},
		{name: "svg", raw: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), want: KindDiagram},
		{name: "log", raw: []byte("INFO started\n"), mime: "text/plain", want: KindLog},
		{name: "zip", raw: []byte{0x50, 0x4b, 0x03, 0x04, 0, 0, 0, 0}, want: KindArchive},
		{name: "build", raw: []byte{0x00, 0x01, 0x02, 0x03}, kind: KindBuild, want: KindBuild},
		{name: "empty", raw: nil, err: ErrEmpty},
		{name: "unknown", raw: []byte{1, 2, 3}, err: ErrUnsupported},
		{name: "kind mismatch", raw: pngRaw, kind: KindPDF, err: ErrKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _, err := Classify(tc.raw, tc.mime, tc.kind)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != tc.want {
				t.Fatalf("kind = %q, want %q", kind, tc.want)
			}
		})
	}
}

func TestSelectForProvider(t *testing.T) {
	ok := Capabilities{Images: true}
	if err := SelectForProvider(KindImage, "image/png", ok); err != nil {
		t.Fatal(err)
	}
	if err := SelectForProvider(KindImage, "image/jpg", ok); err != nil {
		t.Fatal(err)
	}
	err := SelectForProvider(KindPDF, "application/pdf", ok)
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "cannot be sent") {
		t.Fatalf("pdf err = %v", err)
	}
	err = SelectForProvider(KindImage, "image/png", Capabilities{})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("no-vision err = %v", err)
	}
	err = SelectForProvider(KindImage, "image/tiff", ok)
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "tiff") {
		t.Fatalf("tiff err = %v", err)
	}
}

func TestStorePutGetDedupAndSize(t *testing.T) {
	s := openTestStore(t)
	pngRaw := encodeSolidPNG(t, 1, 1, color.RGBA{G: 255, A: 255})
	meta, err := s.Put(pngRaw, PutInput{Name: "shot.png", SessionID: "s1", Links: []Link{{Type: "plan", ID: "p1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Kind != KindImage || meta.MIME != "image/png" || meta.Bytes != int64(len(pngRaw)) {
		t.Fatalf("meta = %+v", meta)
	}
	if _, ok := ParseRef(RefFor(meta.SHA256)); !ok {
		t.Fatalf("ref %q", RefFor(meta.SHA256))
	}
	again, err := s.Put(pngRaw, PutInput{Name: "other.png"})
	if err != nil {
		t.Fatal(err)
	}
	if again.SHA256 != meta.SHA256 {
		t.Fatalf("dedup sha %q vs %q", again.SHA256, meta.SHA256)
	}
	got, loaded, err := s.Get(RefFor(meta.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pngRaw) || loaded.Name != "shot.png" || len(loaded.Links) != 1 {
		t.Fatalf("get = %d bytes meta=%+v", len(got), loaded)
	}
	_, err = s.Put(pngRaw, PutInput{MaxBytes: 4})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size err = %v", err)
	}
}

func TestStoreRetention(t *testing.T) {
	s := openTestStore(t)
	s.now = func() time.Time { return time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC) }
	a, err := s.Put([]byte("%PDF-1.4 a\n"), PutInput{})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 8, 16, 0, 0, 1, 0, time.UTC) }
	b, err := s.Put([]byte("%PDF-1.4 b\n"), PutInput{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ApplyRetention(RetentionPolicy{MaxFiles: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != RefFor(a.SHA256) {
		t.Fatalf("deleted = %v", res.Deleted)
	}
	if _, _, err := s.Get(RefFor(a.SHA256)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old get err = %v", err)
	}
	if _, _, err := s.Get(RefFor(b.SHA256)); err != nil {
		t.Fatal(err)
	}
}

func TestRedactRegionsAndName(t *testing.T) {
	raw := encodeSolidPNG(t, 4, 4, color.RGBA{R: 200, G: 10, B: 10, A: 255})
	out, err := RedactRegions(raw, "image/png", []Region{{X: 0, Y: 0, W: 2, H: 2}})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.At(0, 0); !isBlack(got) {
		t.Fatalf("redacted pixel = %v", got)
	}
	if got := img.At(3, 3); isBlack(got) {
		t.Fatalf("untouched pixel became black: %v", got)
	}
	_, err = RedactRegions(raw, "image/webp", []Region{{X: 0, Y: 0, W: 1, H: 1}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("webp err = %v", err)
	}
	secret := "Authorization: Bearer [REDACTED]"
	name := RedactName(secret)
	if strings.Contains(name, "tok_abc1234567890") {
		t.Fatalf("name leaked secret: %q", name)
	}
}

func TestCompareEvidence(t *testing.T) {
	red := encodeSolidPNG(t, 3, 3, color.RGBA{R: 255, A: 255})
	blue := encodeSolidPNG(t, 3, 3, color.RGBA{B: 255, A: 255})
	same := Compare(red, append([]byte(nil), red...))
	if !same.Equal || same.SHA256A != same.SHA256B || same.Width != 3 {
		t.Fatalf("same = %+v", same)
	}
	diff := Compare(red, blue)
	if diff.Equal || diff.Method != "pixel" || diff.DiffPixels == 0 {
		t.Fatalf("diff = %+v", diff)
	}
	hashOnly := Compare([]byte("%PDF-1.4 a"), []byte("%PDF-1.4 b"))
	if hashOnly.Equal || hashOnly.Method != "sha256" || hashOnly.Detail == "" {
		t.Fatalf("hash-only = %+v", hashOnly)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func encodeSolidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func isBlack(c color.Color) bool {
	r, g, b, a := c.RGBA()
	return r == 0 && g == 0 && b == 0 && a > 0
}
