package pdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestImages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 20, B: 20, A: 255})
		}
	}
	var pb, jb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jb, img, nil); err != nil {
		t.Fatal(err)
	}
	d := New()
	y := d.Y
	if err := d.Logo(pb.Bytes(), 120, 40); err != nil {
		t.Fatal(err)
	}
	if d.Y >= y {
		t.Fatal("the cursor moves below the logo")
	}
	if err := d.Image(jb.Bytes(), 300, 700, 80, 40); err != nil {
		t.Fatal(err)
	}
	if err := d.Image([]byte("GIF89a"), 0, 0, 1, 1); err == nil {
		t.Fatal("other formats are refused")
	}
	d.Row(10, false, "after the logo")
	out := string(d.Bytes())
	for _, want := range []string{"/XObject << /Im0 7 0 R /Im1 8 0 R >>", "/Filter /FlateDecode", "/Filter /DCTDecode", "/Im0 Do", "/Im1 Do"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if w, h, err := ImageSize(pb.Bytes()); err != nil || w != 40 || h != 20 {
		t.Fatalf("size %d×%d %v", w, h, err)
	}
}
