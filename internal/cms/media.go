package cms

// Images library (FR-CMS-03): uploads go to platform storage as public
// files; the type is sniffed from the bytes (never trusted from the client
// or the file name — SVG, HTML and other active content are rejected), the
// size and pixel count are bounded, and web variants (320, 960, 1920 px
// wide) are generated for JPEG and PNG images. Alt text and caption are kept
// per language.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/storage"
)

// CmsMediaVariant is a resized web version of an image.
type CmsMediaVariant struct {
	Name        string    `json:"name" enum:"small,medium,large"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	FileID      uuid.UUID `json:"fileId"`
	URL         string    `json:"url"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
}

// CmsMedia is an image of the library.
type CmsMedia struct {
	ID          uuid.UUID         `json:"id" db:"id"`
	PropertyID  uuid.UUID         `json:"propertyId" db:"property_id"`
	FileID      uuid.UUID         `json:"fileId" db:"file_id"`
	URL         string            `json:"url" db:"-"`
	Filename    string            `json:"filename" db:"filename"`
	ContentType string            `json:"contentType" db:"content_type"`
	SizeBytes   int64             `json:"sizeBytes" db:"size_bytes"`
	Width       *int              `json:"width" db:"width"`
	Height      *int              `json:"height" db:"height"`
	Checksum    string            `json:"checksum" db:"checksum"`
	Alt         map[string]string `json:"alt" db:"alt" doc:"Alternative text per language"`
	Caption     map[string]string `json:"caption" db:"caption"`
	Tags        []string          `json:"tags" db:"tags"`
	Folder      *string           `json:"folder" db:"folder"`
	Variants    []CmsMediaVariant `json:"variants" db:"variants"`
	CreatedAt   time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt   time.Time         `json:"updatedAt" db:"updated_at"`
}

// CmsMediaUpdate edits alt texts, captions, tags and folder.
type CmsMediaUpdate struct {
	Alt     map[string]string `json:"alt,omitempty"`
	Caption map[string]string `json:"caption,omitempty"`
	Tags    []string          `json:"tags,omitempty"`
	Folder  *string           `json:"folder,omitempty"`
}

const mediaSelect = `SELECT id, property_id, file_id, filename, content_type, size_bytes, width, height, checksum, alt, caption, tags, folder,
	variants, created_at, updated_at FROM cms.media`

func withURL(list []CmsMedia) []CmsMedia {
	for i := range list {
		list[i].URL = storage.URLFor(list[i].FileID)
		if list[i].Variants == nil {
			list[i].Variants = []CmsMediaVariant{}
		}
	}
	return list
}

func getMedia(ctx context.Context, q pgx.Tx, mid uuid.UUID) (CmsMedia, error) {
	rows, err := q.Query(ctx, mediaSelect+` WHERE id = $1 AND archived_at IS NULL`, mid)
	md, err := handle.One[CmsMedia](rows, err, "image")
	if err != nil {
		return md, err
	}
	return withURL([]CmsMedia{md})[0], nil
}

var allowedImages = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}

// maxPixels bounds decompression (decompression bombs).
const maxPixels = 50_000_000

var variantWidths = []struct {
	name  string
	width int
}{{"large", 1920}, {"medium", 960}, {"small", 320}}

// Upload stores an image with its web variants.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := handle.Property(ctx)
	var pol ContentPolicy
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		pol, err = Policy(ctx, tx, pid)
		return err
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := int64(pol.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+256<<10)
	if err := r.ParseMultipartForm(limit); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", fmt.Sprintf("upload must be multipart/form-data up to %d MB", pol.MaxUploadMB)))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose an image")))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit || len(data) == 0 {
		httpx.WriteError(w, r, errs.Validation("file_size", "image too large or empty", errs.Field("file", "size", fmt.Sprintf("up to %d MB", pol.MaxUploadMB))))
		return
	}
	ctype := http.DetectContentType(data)
	ext, ok := allowedImages[ctype]
	if !ok {
		httpx.WriteError(w, r, errs.Validation("file_type", "unsupported file type", errs.Field("file", "type", "JPEG, PNG, GIF or WebP images only")))
		return
	}
	width, height, err := imageSize(ctype, data)
	if err != nil || width <= 0 || height <= 0 || width*height > maxPixels {
		httpx.WriteError(w, r, errs.Validation("file_invalid", "the image cannot be read or is too large", errs.Field("file", "invalid", "a valid image up to 50 megapixels")))
		return
	}
	alt, caption := map[string]string{}, map[string]string{}
	for k, vals := range r.MultipartForm.Value {
		if len(vals) == 0 {
			continue
		}
		switch {
		case k == "alt":
			alt[pol.DefaultLanguage] = vals[0]
		case strings.HasPrefix(k, "alt."):
			alt[strings.TrimPrefix(k, "alt.")] = vals[0]
		case k == "caption":
			caption[pol.DefaultLanguage] = vals[0]
		case strings.HasPrefix(k, "caption."):
			caption[strings.TrimPrefix(k, "caption.")] = vals[0]
		}
	}
	if err := checkTexts(pol, alt, "alt", 300); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := checkTexts(pol, caption, "caption", 500); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var tags []string
	for _, t := range strings.Split(r.FormValue("tags"), ",") {
		if t = strings.ToLower(plainText(t)); t != "" && !slices.Contains(tags, t) && len(tags) < 20 {
			tags = append(tags, t)
		}
	}
	folder := nz(plainText(r.FormValue("folder")))
	base := strings.TrimSuffix(filepath.Base(hdr.Filename), filepath.Ext(hdr.Filename))
	if base = slugify(base); base == "" {
		base = "image"
	}
	sum := sha256.Sum256(data)
	variants, err := makeVariants(ctype, data, width)
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_invalid", "the image cannot be read", errs.Field("file", "invalid", err.Error())))
		return
	}
	var out CmsMedia
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		orig, err := m.Files.Save(ctx, tx, base+ext, ctype, "attachment", true, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		vs := []CmsMediaVariant{}
		for _, v := range variants {
			f, err := m.Files.Save(ctx, tx, fmt.Sprintf("%s-%d%s", base, v.width, v.ext), v.ctype, "attachment", true, bytes.NewReader(v.data), int64(len(v.data)))
			if err != nil {
				return err
			}
			vs = append(vs, CmsMediaVariant{Name: v.name, Width: v.width, Height: v.height, FileID: f.ID, URL: f.URL, ContentType: v.ctype,
				SizeBytes: int64(len(v.data))})
		}
		mid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO cms.media (id, property_id, file_id, filename, content_type, size_bytes, width, height, checksum, alt,
			caption, tags, folder, variants, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,coalesce($12, '{}'::text[]),$13,$14,$15,$15)`,
			mid, pid, orig.ID, plainText(hdr.Filename), ctype, len(data), width, height, hex.EncodeToString(sum[:]), alt, caption, tags, folder, vs,
			actorID(ctx)); err != nil {
			return err
		}
		if out, err = getMedia(ctx, tx, mid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: audit.ActionCreate, EntityType: "cms.media", EntityID: mid.String(),
			EntityLabel: out.Filename, PropertyID: &pid, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func checkTexts(pol ContentPolicy, texts map[string]string, field string, max int) error {
	for lang, s := range texts {
		if !slices.Contains(pol.SupportedLanguages, lang) {
			return handle.Invalid(field+"."+lang, "unsupported_language", "language "+lang+" is not a website language")
		}
		s = plainText(s)
		if len([]rune(s)) > max {
			return handle.Invalid(field+"."+lang, "too_long", fmt.Sprintf("at most %d characters", max))
		}
		if s == "" {
			delete(texts, lang)
			continue
		}
		texts[lang] = s
	}
	return nil
}

// imageSize reads the dimensions without decoding the pixels.
func imageSize(ctype string, data []byte) (int, int, error) {
	switch ctype {
	case "image/jpeg":
		c, err := jpeg.DecodeConfig(bytes.NewReader(data))
		return c.Width, c.Height, err
	case "image/png":
		c, err := png.DecodeConfig(bytes.NewReader(data))
		return c.Width, c.Height, err
	case "image/gif":
		c, err := gif.DecodeConfig(bytes.NewReader(data))
		return c.Width, c.Height, err
	case "image/webp":
		return webpSize(data)
	}
	return 0, 0, fmt.Errorf("unsupported image")
}

// webpSize parses the VP8 / VP8L / VP8X header of a WebP image.
func webpSize(b []byte) (int, int, error) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("not a WebP image")
	}
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, fmt.Errorf("invalid VP8 header")
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), nil
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, fmt.Errorf("invalid VP8L header")
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, nil
	}
	return 0, 0, fmt.Errorf("unknown WebP format")
}

type variant struct {
	name          string
	width, height int
	ext, ctype    string
	data          []byte
}

// makeVariants resizes JPEG and PNG images to the web widths (GIF keeps its
// animation, WebP is already a web format: originals only).
func makeVariants(ctype string, data []byte, width int) ([]variant, error) {
	if ctype != "image/jpeg" && ctype != "image/png" {
		return nil, nil
	}
	need := false
	for _, vw := range variantWidths {
		if vw.width < width {
			need = true
		}
	}
	if !need {
		return nil, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	src := toRGBA(img)
	var out []variant
	for _, vw := range variantWidths {
		if vw.width >= src.Bounds().Dx() {
			continue
		}
		dst := downscale(src, vw.width)
		var buf bytes.Buffer
		v := variant{name: vw.name, width: dst.Bounds().Dx(), height: dst.Bounds().Dy()}
		if ctype == "image/png" {
			if err := png.Encode(&buf, dst); err != nil {
				return nil, err
			}
			v.ext, v.ctype = ".png", "image/png"
		} else {
			if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
				return nil, err
			}
			v.ext, v.ctype = ".jpg", "image/jpeg"
		}
		v.data = buf.Bytes()
		out = append(out, v)
		src = dst // the next (smaller) variant is computed from this one
	}
	return out, nil
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// downscale resizes to width w with a box (area-average) filter.
func downscale(src *image.RGBA, w int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	h := max(1, (sh*w+sw/2)/sw)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				off := sy*src.Stride + x0*4
				for sx := x0; sx < x1; sx++ {
					r += uint64(src.Pix[off])
					g += uint64(src.Pix[off+1])
					b += uint64(src.Pix[off+2])
					a += uint64(src.Pix[off+3])
					off += 4
					n++
				}
			}
			d := y*dst.Stride + x*4
			dst.Pix[d], dst.Pix[d+1], dst.Pix[d+2], dst.Pix[d+3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}

// UpdateMedia edits the texts of an image.
func (m *Module) UpdateMedia(ctx context.Context, tx pgx.Tx, mid uuid.UUID, in CmsMediaUpdate) (CmsMedia, error) {
	before, err := getMedia(ctx, tx, mid)
	if err != nil {
		return before, err
	}
	pol, err := Policy(ctx, tx, before.PropertyID)
	if err != nil {
		return before, err
	}
	alt, caption, tags, folder := before.Alt, before.Caption, before.Tags, before.Folder
	if in.Alt != nil {
		if err := checkTexts(pol, in.Alt, "alt", 300); err != nil {
			return before, err
		}
		alt = in.Alt
	}
	if in.Caption != nil {
		if err := checkTexts(pol, in.Caption, "caption", 500); err != nil {
			return before, err
		}
		caption = in.Caption
	}
	if in.Tags != nil {
		tags = nil
		for _, t := range in.Tags {
			if t = strings.ToLower(plainText(t)); t != "" && !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	if in.Folder != nil {
		folder = nz(plainText(*in.Folder))
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.media SET alt = $2, caption = $3, tags = coalesce($4, '{}'::text[]), folder = $5, updated_by = $6 WHERE id = $1`,
		mid, alt, caption, tags, folder, actorID(ctx)); err != nil {
		return before, err
	}
	out, err := getMedia(ctx, tx, mid)
	if err != nil {
		return out, err
	}
	if err := m.touchSite(ctx, tx, out.PropertyID); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: audit.ActionUpdate, EntityType: "cms.media", EntityID: mid.String(),
		EntityLabel: out.Filename, PropertyID: &out.PropertyID, Before: before, After: out})
}

// mediaInUse reports content still showing an image.
func mediaInUse(ctx context.Context, tx pgx.Tx, mid uuid.UUID) (bool, error) {
	needle := mid.String()
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cms.contents c JOIN cms.content_versions v ON v.content_id = c.id
		AND v.version_no IN (c.latest_version, coalesce(c.published_version, 0), coalesce(c.approved_version, 0))
		WHERE c.archived_at IS NULL AND strpos(v.document::text, $1) > 0)
		OR EXISTS (SELECT 1 FROM cms.course_guides WHERE archived_at IS NULL AND $1 = ANY(media_ids))`, needle).Scan(&used)
	return used, err
}

// DeleteMedia archives an unused image.
func (m *Module) DeleteMedia(ctx context.Context, tx pgx.Tx, mid uuid.UUID) error {
	before, err := getMedia(ctx, tx, mid)
	if err != nil {
		return err
	}
	used, err := mediaInUse(ctx, tx, mid)
	if err != nil {
		return err
	}
	if used {
		return errs.Conflict("in_use", "the image is used by website content; replace it there first")
	}
	if _, err := tx.Exec(ctx, `UPDATE cms.media SET archived_at = now(), updated_by = $2 WHERE id = $1`, mid, actorID(ctx)); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "cms", Action: audit.ActionArchive, EntityType: "cms.media", EntityID: mid.String(),
		EntityLabel: before.Filename, PropertyID: &before.PropertyID, Before: before})
}

// mediaMap loads library items by id (public rendering).
func mediaMap(ctx context.Context, tx pgx.Tx, property uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]CmsMedia, error) {
	out := map[uuid.UUID]CmsMedia{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := handle.List[CmsMedia](tx.Query(ctx, mediaSelect+` WHERE property_id = $1 AND id = ANY($2) AND archived_at IS NULL`, property, ids))
	if err != nil {
		return nil, err
	}
	for _, md := range withURL(list) {
		out[md.ID] = md
	}
	return out, nil
}

var _ = json.Marshal
