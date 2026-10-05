package cms

// Content blocks (FR-CMS-01): rich text, image, gallery, video embed, CTA,
// FAQ, contact form, map, banner slot and DATA blocks. A data block stores
// only a reference to structured data — source + filter + limit (contract
// K5) — never a copy: the public page API returns where the website fetches
// the data (the public endpoint of the owning module), so a rate changed in
// the Pricing Engine or a promotion that expires shows on the website
// without editing the CMS (EP-24 acceptance criteria).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// Block types.
var BlockTypes = []string{"rich_text", "image", "gallery", "video", "cta", "faq", "contact_form", "map", "data", "banner_slot"}

type fieldKind int

const (
	fText fieldKind = iota // plain text
	fHTML                  // sanitised rich text
	fEnum
	fUUID
	fUUIDList
	fInt
	fNumber
	fBool
	fURL
	fLink    // CmsLink object
	fFAQ     // [{question, answer}]
	fFilter  // data block filter object
	fStrings // list of plain texts
)

type fieldSpec struct {
	Kind     fieldKind
	Required bool
	Max      int
	Enum     []string
	Min, Lim float64 // numeric bounds (Lim 0 = none)
	// Translatable content that every complete translation must have when
	// the default language has it.
	Translate bool
}

type blockSpec struct {
	Config  map[string]fieldSpec
	Content map[string]fieldSpec
}

var blockSpecs = map[string]blockSpec{
	"rich_text": {Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "html": {Kind: fHTML, Max: 50000, Translate: true}}},
	"image": {Config: map[string]fieldSpec{"mediaId": {Kind: fUUID, Required: true}, "layout": {Kind: fEnum, Enum: []string{"full", "wide", "inline"}},
		"link": {Kind: fLink}},
		Content: map[string]fieldSpec{"alt": {Kind: fText, Max: 300}, "caption": {Kind: fText, Max: 500, Translate: true}}},
	"gallery": {Config: map[string]fieldSpec{"galleryId": {Kind: fUUID}, "mediaIds": {Kind: fUUIDList}, "layout": {Kind: fEnum, Enum: []string{"grid", "carousel", "masonry"}},
		"limit": {Kind: fInt, Min: 1, Lim: 100}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}}},
	"video": {Config: map[string]fieldSpec{"url": {Kind: fURL, Required: true}, "autoplay": {Kind: fBool}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "caption": {Kind: fText, Max: 500, Translate: true}}},
	"cta": {Config: map[string]fieldSpec{"link": {Kind: fLink, Required: true}, "style": {Kind: fEnum, Enum: []string{"primary", "secondary", "link"}},
		"mediaId": {Kind: fUUID}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "text": {Kind: fText, Max: 1000, Translate: true},
			"label": {Kind: fText, Max: 80, Required: true, Translate: true}}},
	"faq": {Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "items": {Kind: fFAQ, Required: true, Translate: true}}},
	"contact_form": {Config: map[string]fieldSpec{"endpoint": {Kind: fEnum, Enum: []string{"contact", "inquiry"}},
		"topic": {Kind: fText, Max: 60},
		"line":  {Kind: fEnum, Enum: []string{"wedding", "banquet", "mice", "corporate_golf", "tournament", "membership", "bungalow", "event", "package", "other"}}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "intro": {Kind: fText, Max: 1000, Translate: true},
			"submitLabel": {Kind: fText, Max: 60, Translate: true}, "successMessage": {Kind: fText, Max: 500, Translate: true}}},
	"map": {Config: map[string]fieldSpec{"contactId": {Kind: fUUID}, "latitude": {Kind: fNumber, Min: -90, Lim: 90}, "longitude": {Kind: fNumber, Min: -180, Lim: 180},
		"zoom": {Kind: fInt, Min: 1, Lim: 20}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}}},
	"data": {Config: map[string]fieldSpec{"source": {Kind: fEnum, Required: true}, "filter": {Kind: fFilter}, "limit": {Kind: fInt, Min: 1, Lim: 50},
		"layout": {Kind: fEnum, Enum: []string{"grid", "list", "table", "carousel", "calendar", "widget"}}},
		Content: map[string]fieldSpec{"heading": {Kind: fText, Max: 200, Translate: true}, "intro": {Kind: fText, Max: 1000, Translate: true},
			"emptyText": {Kind: fText, Max: 300, Translate: true}}},
	"banner_slot": {Config: map[string]fieldSpec{"placement": {Kind: fEnum, Required: true, Enum: BannerPlacements}}},
}

// ── structured data sources (contract K5) ─────────────────────────────────

// CmsDataFilter is one filter of a data source.
type CmsDataFilter struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Type      string   `json:"type" enum:"string,enum,date,uuid,int,bool"`
	Enum      []string `json:"enum,omitempty"`
	Required  bool     `json:"required"`
	PathParam bool     `json:"pathParam" doc:"Part of the endpoint path rather than the query string"`
}

// CmsDataSource is structured data a data block may reference; the data stays
// with its owner module and is read by the website from Endpoint.
type CmsDataSource struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Owner       string          `json:"owner" doc:"Module that owns the data"`
	Endpoint    string          `json:"endpoint" doc:"Public API the website reads (path parameters in braces)"`
	Description string          `json:"description"`
	Filters     []CmsDataFilter `json:"filters"`
	MaxLimit    int             `json:"maxLimit"`
	CMSMenu     string          `json:"cmsMenu,omitempty" doc:"CMS menu (Naming Convention §26) showing where this data is used"`
	// endpointFor picks the endpoint from the filter (e.g. the golf rate
	// table lives in golf, the other rate tables in commercial).
	endpointFor func(f map[string]string) string
}

var lines = []string{"golf", "sportclub", "stay", "meeting", "range", "vouchers"}

// DataSources is the registry of structured data (K5 + earlier public APIs).
var DataSources = []CmsDataSource{
	{Key: "rates", Label: "Rate table (Pricing Engine)", Owner: "commercial", Endpoint: "/api/v1/public/rates/{line}", MaxLimit: 50, CMSMenu: "Pricing",
		Description: "Structured rate table of a line from the Pricing Engine — never an image of a flyer (Product Overview §38)",
		Filters: []CmsDataFilter{{Key: "line", Label: "Line", Type: "enum", Enum: lines, Required: true, PathParam: true},
			{Key: "date", Label: "Date (golf)", Type: "date"}},
		endpointFor: func(f map[string]string) string {
			if f["line"] == "golf" {
				return "/api/v1/public/golf/rates"
			}
			return "/api/v1/public/rates/{line}"
		}},
	{Key: "packages", Label: "Packages", Owner: "commercial", Endpoint: "/api/v1/public/packages", MaxLimit: 50, CMSMenu: "Packages",
		Description: "Published packages; a code shows one package",
		Filters: []CmsDataFilter{{Key: "code", Label: "Package code (one package)", Type: "string", PathParam: true}, {Key: "line", Label: "Line", Type: "string"},
			{Key: "category", Label: "Category", Type: "string"}},
		endpointFor: func(f map[string]string) string {
			if f["code"] != "" {
				return "/api/v1/public/packages/{code}"
			}
			return "/api/v1/public/packages"
		}},
	{Key: "promotions", Label: "Promotions / What's On", Owner: "commercial", Endpoint: "/api/v1/public/promotions", MaxLimit: 50, CMSMenu: "Promotions",
		Description: "Promotions in their validity period only — they appear and disappear automatically (FR-CMS-07)",
		Filters:     []CmsDataFilter{{Key: "line", Label: "Line", Type: "string"}, {Key: "category", Label: "Category", Type: "string"}}},
	{Key: "events", Label: "Events", Owner: "banquet", Endpoint: "/api/v1/public/events", MaxLimit: 50, CMSMenu: "Events",
		Description: "Public events open for registration; an id shows one event",
		Filters: []CmsDataFilter{{Key: "id", Label: "Event (one event)", Type: "uuid", PathParam: true}, {Key: "eventType", Label: "Event type", Type: "string"},
			{Key: "from", Label: "From", Type: "date"}, {Key: "to", Label: "To", Type: "date"}},
		endpointFor: func(f map[string]string) string {
			if f["id"] != "" {
				return "/api/v1/public/events/{id}"
			}
			return "/api/v1/public/events"
		}},
	{Key: "tournaments", Label: "Tournaments", Owner: "golf", Endpoint: "/api/v1/public/tournaments", MaxLimit: 50, CMSMenu: "Events",
		Description: "Public tournaments; an id shows one tournament, with leaderboard its public leaderboard",
		Filters: []CmsDataFilter{{Key: "id", Label: "Tournament (one tournament)", Type: "uuid", PathParam: true},
			{Key: "leaderboard", Label: "Show the leaderboard", Type: "bool"}, {Key: "status", Label: "Status", Type: "string"}},
		endpointFor: func(f map[string]string) string {
			switch {
			case f["id"] != "" && f["leaderboard"] == "true":
				return "/api/v1/public/tournaments/{id}/leaderboard"
			case f["id"] != "":
				return "/api/v1/public/tournaments/{id}"
			}
			return "/api/v1/public/tournaments"
		}},
	{Key: "hall_of_fame", Label: "Hall of Fame", Owner: "golf", Endpoint: "/api/v1/public/hall-of-fame", MaxLimit: 50,
		Description: "Published, consented Hall of Fame entries",
		Filters:     []CmsDataFilter{{Key: "category", Label: "Category", Type: "string"}}},
	{Key: "course_guide", Label: "Course Guide / Hole-by-Hole", Owner: "golf", Endpoint: "/api/v1/public/golf/info", MaxLimit: 36, CMSMenu: "Course Guide",
		Description: "Course data (par, distances) from golf, completed by the Course Guide texts of the CMS (/api/v1/public/cms/course-guide)",
		Filters:     []CmsDataFilter{{Key: "course", Label: "Course code", Type: "string"}, {Key: "hole", Label: "Hole", Type: "int"}}},
	{Key: "membership_types", Label: "Membership types", Owner: "membership", Endpoint: "/api/v1/public/membership-types", MaxLimit: 50,
		Description: "Membership programs, types, packages and benefits",
		Filters:     []CmsDataFilter{{Key: "programKind", Label: "Program kind", Type: "string"}}},
	{Key: "availability", Label: "Availability widget", Owner: "reservation", Endpoint: "/api/v1/public/availability", MaxLimit: 50,
		Description: "Live availability with prices for booking (tee times, courts, bungalows, meeting rooms)",
		Filters: []CmsDataFilter{{Key: "resourceType", Label: "Resource type", Type: "enum", Required: true,
			Enum: []string{"tee_time", "sport_court", "bungalow", "meeting_room", "vip_suite"}}},
		endpointFor: func(f map[string]string) string {
			switch f["resourceType"] {
			case "tee_time":
				return "/api/v1/public/golf/availability"
			case "bungalow":
				return "/api/v1/public/bungalow-availability"
			}
			return "/api/v1/public/availability"
		}},
	{Key: "reciprocal_clubs", Label: "Reciprocal clubs", Owner: "golf", Endpoint: "/api/v1/public/reciprocal-clubs", MaxLimit: 50},
	{Key: "stay_units", Label: "Bungalow, VIP Suite & Meeting rooms", Owner: "stay", Endpoint: "/api/v1/public/stay", MaxLimit: 50},
	{Key: "sport_club", Label: "Sport Club facilities", Owner: "sportclub", Endpoint: "/api/v1/public/sport-club", MaxLimit: 50},
	{Key: "news", Label: "News", Owner: "cms", Endpoint: "/api/v1/public/cms/news", MaxLimit: 24, CMSMenu: "News",
		Filters: []CmsDataFilter{{Key: "category", Label: "Category code", Type: "string"}, {Key: "tag", Label: "Tag", Type: "string"},
			{Key: "featured", Label: "Featured only", Type: "bool"}}},
	{Key: "gallery", Label: "Gallery albums", Owner: "cms", Endpoint: "/api/v1/public/cms/gallery", MaxLimit: 24, CMSMenu: "Gallery",
		Filters: []CmsDataFilter{{Key: "slug", Label: "Album (slug)", Type: "string", PathParam: true}},
		endpointFor: func(f map[string]string) string {
			if f["slug"] != "" {
				return "/api/v1/public/cms/gallery/{slug}"
			}
			return "/api/v1/public/cms/gallery"
		}},
}

func dataSource(key string) (CmsDataSource, bool) {
	for _, s := range DataSources {
		if s.Key == key {
			return s, true
		}
	}
	return CmsDataSource{}, false
}

func dataSourceKeys() []string {
	out := make([]string, len(DataSources))
	for i, s := range DataSources {
		out[i] = s.Key
	}
	return out
}

var dateOnlyRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// checkFilter validates and normalises a data block filter to strings.
func checkFilter(src CmsDataSource, raw any, field string) (map[string]string, error) {
	out := map[string]string{}
	if raw != nil {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, handle.Invalid(field, "invalid", "filter must be an object")
		}
		for k, v := range obj {
			var spec *CmsDataFilter
			for i := range src.Filters {
				if src.Filters[i].Key == k {
					spec = &src.Filters[i]
				}
			}
			if spec == nil {
				return nil, handle.Invalid(field+"."+k, "unknown_filter", fmt.Sprintf("%s does not filter by %s", src.Label, k))
			}
			s := strings.TrimSpace(fmt.Sprint(v))
			if v == nil || s == "" {
				continue
			}
			switch spec.Type {
			case "enum":
				if !slices.Contains(spec.Enum, s) {
					return nil, handle.Invalid(field+"."+k, "invalid_option", "must be one of: "+strings.Join(spec.Enum, ", "))
				}
			case "date":
				if _, err := time.Parse("2006-01-02", s); err != nil || !dateOnlyRe.MatchString(s) {
					return nil, handle.Invalid(field+"."+k, "invalid_date", "must be a date (YYYY-MM-DD)")
				}
			case "uuid":
				if _, err := uuid.Parse(s); err != nil {
					return nil, handle.Invalid(field+"."+k, "invalid_id", "must be an id")
				}
			case "int":
				if _, err := strconv.Atoi(s); err != nil {
					return nil, handle.Invalid(field+"."+k, "invalid_type", "must be a whole number")
				}
			case "bool":
				b, err := strconv.ParseBool(s)
				if err != nil {
					return nil, handle.Invalid(field+"."+k, "invalid_type", "must be true or false")
				}
				s = strconv.FormatBool(b)
			default:
				if len(s) > 80 || strings.ContainsAny(s, "/?#&<>\"\\") {
					return nil, handle.Invalid(field+"."+k, "invalid", "must be a short code without URL characters")
				}
			}
			out[k] = s
		}
	}
	for _, f := range src.Filters {
		if f.Required && out[f.Key] == "" {
			return nil, handle.Invalid(field+"."+f.Key, "required", f.Label+" is required for "+src.Label)
		}
	}
	return out, nil
}

// CmsDataRef tells the website where to fetch the data of a data block.
type CmsDataRef struct {
	Source   string            `json:"source"`
	Owner    string            `json:"owner"`
	Endpoint string            `json:"endpoint" doc:"Resolved public API path"`
	Query    map[string]string `json:"query"`
	URL      string            `json:"url" doc:"Endpoint with query string"`
	Limit    int               `json:"limit"`
}

// dataRef resolves the endpoint of a data block for a property.
func dataRef(config map[string]any, property uuid.UUID, propertyCode, lang string) *CmsDataRef {
	key, _ := config["source"].(string)
	src, ok := dataSource(key)
	if !ok {
		return nil
	}
	filter := map[string]string{}
	if f, ok := config["filter"].(map[string]any); ok {
		for k, v := range f {
			filter[k] = fmt.Sprint(v)
		}
	}
	endpoint := src.Endpoint
	if src.endpointFor != nil {
		endpoint = src.endpointFor(filter)
	}
	q := map[string]string{}
	for _, f := range src.Filters {
		v := filter[f.Key]
		if v == "" {
			continue
		}
		if strings.Contains(endpoint, "{"+f.Key+"}") {
			endpoint = strings.ReplaceAll(endpoint, "{"+f.Key+"}", url.PathEscape(v))
			continue
		}
		if f.PathParam || f.Key == "leaderboard" {
			continue
		}
		q[f.Key] = v
	}
	if key == "availability" && (filter["resourceType"] == "tee_time" || filter["resourceType"] == "bungalow") {
		delete(q, "resourceType") // dedicated endpoints
	}
	limit := src.MaxLimit
	if n, ok := toInt(config["limit"]); ok && n > 0 && n < limit {
		limit = n
	}
	// The P1 golf website API names the property by code, the others by id.
	if strings.HasPrefix(endpoint, "/api/v1/public/golf/") {
		q["property"] = propertyCode
	} else {
		q["propertyId"] = property.String()
	}
	if src.Owner == "cms" {
		q["lang"] = lang
	}
	q["limit"] = strconv.Itoa(limit)
	vals := url.Values{}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals.Set(k, q[k])
	}
	return &CmsDataRef{Source: key, Owner: src.Owner, Endpoint: endpoint, Query: q, URL: endpoint + "?" + vals.Encode(), Limit: limit}
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// ── block validation ──────────────────────────────────────────────────────

// refs collects references to check against the database.
type refs struct {
	media     []uuid.UUID
	pages     []uuid.UUID
	galleries []uuid.UUID
	contacts  []uuid.UUID
}

func (r *refs) add(list *[]uuid.UUID, u uuid.UUID) {
	if !slices.Contains(*list, u) {
		*list = append(*list, u)
	}
}

// checkBlocks validates and normalises blocks (sanitises HTML, generates ids).
func checkBlocks(blocks []CmsBlock, pol ContentPolicy, rf *refs) error {
	if len(blocks) > 200 {
		return handle.Invalid("blocks", "too_many", "a page has at most 200 blocks")
	}
	seen := map[string]bool{}
	for i := range blocks {
		b := &blocks[i]
		p := fmt.Sprintf("blocks[%d]", i)
		spec, ok := blockSpecs[b.Type]
		if !ok {
			return handle.Invalid(p+".type", "invalid_option", "must be one of: "+strings.Join(BlockTypes, ", "))
		}
		if b.ID == "" || seen[b.ID] || len(b.ID) > 64 {
			b.ID = uuid.NewString()
		}
		seen[b.ID] = true
		if b.Config == nil {
			b.Config = map[string]any{}
		}
		for k, v := range b.Config {
			fs, ok := spec.Config[k]
			if !ok {
				return handle.Invalid(p+".config."+k, "unknown_field", "not a setting of a "+b.Type+" block")
			}
			nv, err := checkValue(fs, v, p+".config."+k, rf)
			if err != nil {
				return err
			}
			if nv == nil {
				delete(b.Config, k)
			} else {
				b.Config[k] = nv
			}
		}
		for k, fs := range spec.Config {
			if fs.Required && b.Config[k] == nil {
				return handle.Invalid(p+".config."+k, "required", k+" is required for a "+b.Type+" block")
			}
		}
		switch b.Type {
		case "data":
			src, ok := dataSource(fmt.Sprint(b.Config["source"]))
			if !ok {
				return handle.Invalid(p+".config.source", "invalid_option", "source must be one of: "+strings.Join(dataSourceKeys(), ", "))
			}
			f, err := checkFilter(src, b.Config["filter"], p+".config.filter")
			if err != nil {
				return err
			}
			fm := map[string]any{}
			for k, v := range f {
				fm[k] = v
			}
			b.Config["filter"] = fm
			if n, ok := toInt(b.Config["limit"]); ok && n > src.MaxLimit {
				return handle.Invalid(p+".config.limit", "too_large", fmt.Sprintf("at most %d items for %s", src.MaxLimit, src.Label))
			}
		case "gallery":
			if b.Config["galleryId"] == nil && b.Config["mediaIds"] == nil {
				return handle.Invalid(p+".config", "required", "a gallery block shows a gallery album or a list of images")
			}
		case "video":
			if videoEmbed(fmt.Sprint(b.Config["url"])) == "" {
				return handle.Invalid(p+".config.url", "invalid", "only YouTube and Vimeo videos can be embedded")
			}
		case "map":
			if b.Config["contactId"] == nil && (b.Config["latitude"] == nil || b.Config["longitude"] == nil) {
				return handle.Invalid(p+".config", "required", "a map shows a contact's location or latitude and longitude")
			}
		}
		if b.Content == nil {
			b.Content = map[string]map[string]any{}
		}
		for lang, c := range b.Content {
			if !slices.Contains(pol.SupportedLanguages, lang) {
				return handle.Invalid(p+".content."+lang, "unsupported_language", "language "+lang+" is not a website language (Content Policies)")
			}
			for k, v := range c {
				fs, ok := spec.Content[k]
				if !ok {
					return handle.Invalid(p+".content."+lang+"."+k, "unknown_field", "not a text of a "+b.Type+" block")
				}
				nv, err := checkValue(fs, v, p+".content."+lang+"."+k, rf)
				if err != nil {
					return err
				}
				if nv == nil {
					delete(c, k)
				} else {
					c[k] = nv
				}
			}
			if len(c) == 0 {
				delete(b.Content, lang)
			}
		}
		def := b.Content[pol.DefaultLanguage]
		for k, fs := range spec.Content {
			if fs.Required && (def == nil || def[k] == nil) {
				return handle.Invalid(p+".content."+pol.DefaultLanguage+"."+k, "required", k+" is required in the default language")
			}
		}
	}
	return nil
}

// checkValue validates one config / content value and returns its
// normalised form (nil = drop).
func checkValue(fs fieldSpec, v any, field string, rf *refs) (any, error) {
	if v == nil {
		return nil, nil
	}
	str := func() (string, error) {
		s, ok := v.(string)
		if !ok {
			return "", handle.Invalid(field, "invalid_type", "must be text")
		}
		return strings.TrimSpace(s), nil
	}
	switch fs.Kind {
	case fText:
		s, err := str()
		if err != nil {
			return nil, err
		}
		s = plainText(s)
		if fs.Max > 0 && len([]rune(s)) > fs.Max {
			return nil, handle.Invalid(field, "too_long", fmt.Sprintf("must be at most %d characters", fs.Max))
		}
		if s == "" {
			return nil, nil
		}
		return s, nil
	case fHTML:
		s, err := str()
		if err != nil {
			return nil, err
		}
		if fs.Max > 0 && len(s) > fs.Max {
			return nil, handle.Invalid(field, "too_long", fmt.Sprintf("must be at most %d characters", fs.Max))
		}
		s = sanitizeHTML(s)
		if s == "" {
			return nil, nil
		}
		return s, nil
	case fEnum:
		s, err := str()
		if err != nil {
			return nil, err
		}
		if len(fs.Enum) > 0 && !slices.Contains(fs.Enum, s) {
			return nil, handle.Invalid(field, "invalid_option", "must be one of: "+strings.Join(fs.Enum, ", "))
		}
		return s, nil
	case fUUID:
		s, err := str()
		if err != nil {
			return nil, err
		}
		u, perr := uuid.Parse(s)
		if perr != nil {
			return nil, handle.Invalid(field, "invalid_id", "must be an id")
		}
		switch {
		case strings.HasSuffix(field, "galleryId"):
			rf.add(&rf.galleries, u)
		case strings.HasSuffix(field, "contactId"):
			rf.add(&rf.contacts, u)
		default:
			rf.add(&rf.media, u)
		}
		return u.String(), nil
	case fUUIDList:
		list, ok := v.([]any)
		if !ok {
			return nil, handle.Invalid(field, "invalid_type", "must be a list of ids")
		}
		out := []any{}
		for _, x := range list {
			u, err := uuid.Parse(fmt.Sprint(x))
			if err != nil {
				return nil, handle.Invalid(field, "invalid_id", "must be a list of ids")
			}
			rf.add(&rf.media, u)
			out = append(out, u.String())
		}
		if len(out) > 100 {
			return nil, handle.Invalid(field, "too_many", "at most 100 images")
		}
		return out, nil
	case fInt:
		n, ok := toInt(v)
		if !ok || (fs.Lim != 0 && (float64(n) < fs.Min || float64(n) > fs.Lim)) {
			return nil, handle.Invalid(field, "invalid", fmt.Sprintf("must be a whole number between %v and %v", fs.Min, fs.Lim))
		}
		return n, nil
	case fNumber:
		f, ok := toFloat(v)
		if !ok || (fs.Lim != 0 && (f < fs.Min || f > fs.Lim)) {
			return nil, handle.Invalid(field, "invalid", fmt.Sprintf("must be a number between %v and %v", fs.Min, fs.Lim))
		}
		return f, nil
	case fBool:
		b, ok := v.(bool)
		if !ok {
			return nil, handle.Invalid(field, "invalid_type", "must be true or false")
		}
		return b, nil
	case fURL:
		s, err := str()
		if err != nil {
			return nil, err
		}
		if !isHTTPURL(s) {
			return nil, handle.Invalid(field, "invalid", "must be an http(s) address")
		}
		return s, nil
	case fLink:
		raw, _ := json.Marshal(v)
		var l CmsLink
		if err := strictJSON(string(raw), &l); err != nil {
			return nil, handle.Invalid(field, "invalid", "a link is {type: page|url|route, pageId, url, routeKey, newTab}")
		}
		if err := checkLinkShape(&l, field); err != nil {
			return nil, err
		}
		if l.PageID != nil {
			rf.add(&rf.pages, *l.PageID)
		}
		out := map[string]any{"type": l.Type}
		if l.PageID != nil {
			out["pageId"] = l.PageID.String()
		}
		if l.URL != "" {
			out["url"] = l.URL
		}
		if l.RouteKey != "" {
			out["routeKey"] = l.RouteKey
		}
		if l.NewTab {
			out["newTab"] = true
		}
		return out, nil
	case fFAQ:
		list, ok := v.([]any)
		if !ok {
			return nil, handle.Invalid(field, "invalid_type", "FAQ items are [{question, answer}]")
		}
		out := []any{}
		for i, x := range list {
			obj, ok := x.(map[string]any)
			if !ok {
				return nil, handle.Invalid(field, "invalid_type", "FAQ items are [{question, answer}]")
			}
			for k := range obj {
				if k != "question" && k != "answer" {
					return nil, handle.Invalid(fmt.Sprintf("%s[%d].%s", field, i, k), "unknown_field", "FAQ items are [{question, answer}]")
				}
			}
			q := plainText(fmt.Sprint(obj["question"]))
			a := sanitizeHTML(fmt.Sprint(obj["answer"]))
			if obj["question"] == nil || q == "" || obj["answer"] == nil || a == "" {
				return nil, handle.Invalid(fmt.Sprintf("%s[%d]", field, i), "required", "question and answer are required")
			}
			if len([]rune(q)) > 300 || len(a) > 5000 {
				return nil, handle.Invalid(fmt.Sprintf("%s[%d]", field, i), "too_long", "question ≤ 300, answer ≤ 5000 characters")
			}
			out = append(out, map[string]any{"question": q, "answer": a})
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	case fFilter:
		if _, ok := v.(map[string]any); !ok {
			return nil, handle.Invalid(field, "invalid", "filter must be an object")
		}
		return v, nil
	case fStrings:
		list, ok := v.([]any)
		if !ok {
			return nil, handle.Invalid(field, "invalid_type", "must be a list of texts")
		}
		out := []any{}
		for _, x := range list {
			if s := plainText(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	return v, nil
}

var (
	youtubeRe = regexp.MustCompile(`^(?:https?://)?(?:www\.|m\.)?(?:youtube\.com/(?:watch\?(?:.*&)?v=|embed/|shorts/)|youtu\.be/)([A-Za-z0-9_-]{6,20})`)
	vimeoRe   = regexp.MustCompile(`^(?:https?://)?(?:www\.|player\.)?vimeo\.com/(?:video/)?([0-9]{4,12})`)
)

// videoEmbed returns the privacy-friendly embed URL of a YouTube / Vimeo video.
func videoEmbed(u string) string {
	if m := youtubeRe.FindStringSubmatch(u); m != nil {
		return "https://www.youtube-nocookie.com/embed/" + m[1]
	}
	if m := vimeoRe.FindStringSubmatch(u); m != nil {
		return "https://player.vimeo.com/video/" + m[1]
	}
	return ""
}

// ── links ─────────────────────────────────────────────────────────────────

// CmsLink is a link target of a CTA, banner or navigation item.
type CmsLink struct {
	Type     string     `json:"type" enum:"page,url,route"`
	PageID   *uuid.UUID `json:"pageId,omitempty"`
	URL      string     `json:"url,omitempty"`
	RouteKey string     `json:"routeKey,omitempty" doc:"Structured website route (book_golf, membership, …)"`
	NewTab   bool       `json:"newTab,omitempty"`
}

func checkLinkShape(l *CmsLink, field string) error {
	switch l.Type {
	case "page":
		if l.PageID == nil {
			return handle.Invalid(field+".pageId", "required", "choose the page to link to")
		}
		l.URL, l.RouteKey = "", ""
	case "url":
		l.URL = strings.TrimSpace(l.URL)
		if !isHTTPURL(l.URL) && !isSitePath(l.URL) &&
			!strings.HasPrefix(l.URL, "mailto:") && !strings.HasPrefix(l.URL, "tel:") {
			return handle.Invalid(field+".url", "invalid", "a link is an http(s) address, a /path, mailto: or tel:")
		}
		l.PageID, l.RouteKey = nil, ""
	case "route":
		if _, ok := RouteKeys[l.RouteKey]; !ok {
			return handle.Invalid(field+".routeKey", "invalid_option", "must be one of: "+strings.Join(routeKeyNames(), ", "))
		}
		l.PageID, l.URL = nil, ""
	default:
		return handle.Invalid(field+".type", "invalid_option", "must be one of: page, url, route")
	}
	return nil
}

// checkLink validates a link including the existence of a linked page.
func (m *Module) checkLink(ctx context.Context, tx pgx.Tx, l *CmsLink, field string) error {
	if err := checkLinkShape(l, field); err != nil {
		return err
	}
	if l.PageID != nil {
		return checkContents(ctx, tx, handle.Property(ctx), KindPage, []uuid.UUID{*l.PageID}, field+".pageId")
	}
	return nil
}

// checkContents verifies that content items of a kind exist in the property.
func checkContents(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, ids []uuid.UUID, field string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT id) FROM cms.contents WHERE property_id = $1 AND kind = $2 AND id = ANY($3) AND archived_at IS NULL`,
		property, kind, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(uniqueIDs(ids)) {
		return errs.Validation(kind+"_not_found", specOf(kind).Name+" not found", errs.Field(field, "not_found", specOf(kind).Name+" not found in this property"))
	}
	return nil
}

// isSitePath reports a path on this website (not protocol-relative).
func isSitePath(s string) bool {
	return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && pathRe.MatchString(s)
}
