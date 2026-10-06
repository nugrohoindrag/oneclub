package pdf

// Images (branding logos on documents): JPEG files are embedded as they are
// (DCTDecode); PNG files are decoded, composited on white and stored as
// Flate-compressed RGB. Other formats are refused.

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// ErrImageFormat is returned for images other than JPEG and PNG.
var ErrImageFormat = errors.New("pdf: only JPEG and PNG images are supported")

type pdfImage struct {
	w, h       int
	colorSpace string
	filter     string
	data       []byte
}

// ImageSize returns the pixel size of a JPEG or PNG image.
func ImageSize(data []byte) (int, int, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	if format != "jpeg" && format != "png" {
		return 0, 0, ErrImageFormat
	}
	return cfg.Width, cfg.Height, nil
}

func loadImage(data []byte) (pdfImage, error) {
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err == nil {
		cs := "/DeviceRGB"
		switch cfg.ColorModel {
		case color.GrayModel:
			cs = "/DeviceGray"
		case color.CMYKModel:
			return pdfImage{}, ErrImageFormat
		}
		return pdfImage{w: cfg.Width, h: cfg.Height, colorSpace: cs, filter: "/DCTDecode", data: data}, nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return pdfImage{}, ErrImageFormat
	}
	b := img.Bounds()
	raw := make([]byte, 0, b.Dx()*b.Dy()*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA() // premultiplied: composite on white
			white := 0xffff - a
			raw = append(raw, byte((r+white)>>8), byte((g+white)>>8), byte((bl+white)>>8))
		}
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(raw); err != nil {
		return pdfImage{}, err
	}
	if err := zw.Close(); err != nil {
		return pdfImage{}, err
	}
	return pdfImage{w: b.Dx(), h: b.Dy(), colorSpace: "/DeviceRGB", filter: "/FlateDecode", data: z.Bytes()}, nil
}

// Image draws a JPEG or PNG image with its bottom-left corner at (x, y),
// scaled to w × h points.
func (d *Doc) Image(data []byte, x, y, w, h float64) error {
	img, err := loadImage(data)
	if err != nil {
		return err
	}
	d.images = append(d.images, img)
	fmt.Fprintf(d.cur, "q %.2f 0 0 %.2f %.2f %.2f cm /Im%d Do Q\n", w, h, x, y, len(d.images)-1)
	return nil
}

// Logo draws an image at the cursor (left margin) fitted into maxW × maxH
// points with its aspect ratio kept, then moves the cursor below it.
func (d *Doc) Logo(data []byte, maxW, maxH float64) error {
	pw, ph, err := ImageSize(data)
	if err != nil || pw == 0 || ph == 0 {
		if err == nil {
			err = ErrImageFormat
		}
		return err
	}
	w, h := maxW, maxW*float64(ph)/float64(pw)
	if h > maxH {
		w, h = maxH*float64(pw)/float64(ph), maxH
	}
	if err := d.Image(data, Margin, d.Y-h, w, h); err != nil {
		return err
	}
	d.Y -= h + 8
	return nil
}
