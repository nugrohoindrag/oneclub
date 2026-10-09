package experience

// Live weather of a course (demo feedback 9 Oct 2026, OneClub replaces
// Smartscore): read from Open-Meteo (open source, no API key) at the middle
// of the course map and shown on the caddy tablet, the Course Monitor and
// Course Status, where it suggests the weather status the Marshal sets
// (rain, lightning warning, heat warning). Answers are kept 10 minutes per
// course; when the service cannot be reached the screens say so and the
// Marshal keeps setting the status by hand.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// Weather is the current weather at a course.
type Weather struct {
	Available    bool      `json:"available" doc:"False when the course has no map position or the weather service did not answer"`
	Reason       string    `json:"reason,omitempty"`
	Source       string    `json:"source" enum:"open-meteo"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	At           time.Time `json:"at"`
	TemperatureC float64   `json:"temperatureC"`
	Condition    string    `json:"condition" enum:"clear,partly_cloudy,cloudy,fog,drizzle,rain,heavy_rain,thunderstorm,other"`
	WeatherCode  int       `json:"weatherCode" doc:"WMO weather code"`
	PrecipMM     float64   `json:"precipitationMm"`
	WindKmh      float64   `json:"windKmh"`
	RainChance   *int      `json:"rainChanceNextHour" doc:"Percent"`
	Suggested    string    `json:"suggestedStatus" enum:"normal,rain,lightning_warning,heat_warning" doc:"Weather status of Course Status it suggests"`
	Summary      string    `json:"summary"`
}

// WeatherSource reads the current weather at a position.
type WeatherSource interface {
	Current(ctx context.Context, lat, lng float64) (Weather, error)
}

// OpenMeteo reads https://open-meteo.com (no key), cached per position.
type OpenMeteo struct {
	BaseURL string
	Client  *http.Client
	TTL     time.Duration
	mu      sync.Mutex
	cache   map[string]Weather
}

// NewOpenMeteo uses OPEN_METEO_URL when set ("off" disables the lookups).
func NewOpenMeteo() *OpenMeteo {
	base := os.Getenv("OPEN_METEO_URL")
	if base == "" {
		base = "https://api.open-meteo.com"
	}
	return &OpenMeteo{BaseURL: strings.TrimRight(base, "/"), Client: &http.Client{Timeout: 6 * time.Second}, TTL: 10 * time.Minute, cache: map[string]Weather{}}
}

func (o *OpenMeteo) Current(ctx context.Context, lat, lng float64) (Weather, error) {
	if o.BaseURL == "off" {
		return Weather{}, fmt.Errorf("weather lookups are switched off")
	}
	key := fmt.Sprintf("%.3f,%.3f", lat, lng)
	o.mu.Lock()
	if w, ok := o.cache[key]; ok && clock.Now().Sub(w.At) < o.TTL {
		o.mu.Unlock()
		return w, nil
	}
	o.mu.Unlock()
	url := fmt.Sprintf("%s/v1/forecast?latitude=%.5f&longitude=%.5f&current=temperature_2m,precipitation,weather_code,wind_speed_10m"+
		"&hourly=precipitation_probability&forecast_hours=2&timezone=auto", o.BaseURL, lat, lng)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Weather{}, err
	}
	res, err := o.Client.Do(req)
	if err != nil {
		return Weather{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Weather{}, fmt.Errorf("open-meteo answered %d", res.StatusCode)
	}
	var body struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			Precip      float64 `json:"precipitation"`
			Code        int     `json:"weather_code"`
			Wind        float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Hourly struct {
			Rain []*int `json:"precipitation_probability"`
		} `json:"hourly"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return Weather{}, err
	}
	w := Weather{Available: true, Source: "open-meteo", Lat: lat, Lng: lng, At: clock.Now(), TemperatureC: body.Current.Temperature,
		WeatherCode: body.Current.Code, PrecipMM: body.Current.Precip, WindKmh: body.Current.Wind}
	for _, p := range body.Hourly.Rain {
		if p != nil && (w.RainChance == nil || *p > *w.RainChance) {
			v := *p
			w.RainChance = &v
		}
	}
	describe(&w)
	o.mu.Lock()
	o.cache[key] = w
	o.mu.Unlock()
	return w, nil
}

// describe names the WMO code and suggests the Course Status weather.
func describe(w *Weather) {
	c := w.WeatherCode
	switch {
	case c == 0:
		w.Condition = "clear"
	case c == 1 || c == 2:
		w.Condition = "partly_cloudy"
	case c == 3:
		w.Condition = "cloudy"
	case c == 45 || c == 48:
		w.Condition = "fog"
	case c >= 51 && c <= 57:
		w.Condition = "drizzle"
	case c == 65 || c == 82:
		w.Condition = "heavy_rain"
	case (c >= 61 && c <= 67) || c == 80 || c == 81:
		w.Condition = "rain"
	case c >= 95:
		w.Condition = "thunderstorm"
	default:
		w.Condition = "other"
	}
	switch {
	case w.Condition == "thunderstorm":
		w.Suggested = "lightning_warning"
	case w.Condition == "rain" || w.Condition == "heavy_rain" || w.Condition == "drizzle" || w.PrecipMM >= 0.5:
		w.Suggested = "rain"
	case w.TemperatureC >= 35:
		w.Suggested = "heat_warning"
	default:
		w.Suggested = "normal"
	}
	w.Summary = fmt.Sprintf("%.0f°C, %s", w.TemperatureC, strings.ReplaceAll(w.Condition, "_", " "))
	if w.RainChance != nil {
		w.Summary += fmt.Sprintf(", rain %d%% next hour", *w.RainChance)
	}
}

// CourseWeather is the weather at the middle of the course map.
func (m *Module) CourseWeather(ctx context.Context, tx pgx.Tx, course uuid.UUID) (Weather, error) {
	out := Weather{Source: "open-meteo", At: clock.Now()}
	cm, err := loadCourseMap(ctx, tx, course)
	if err != nil {
		return out, err
	}
	if !cm.ok {
		out.Reason = "the course has no geo-referenced course map"
		return out, nil
	}
	out.Lat, out.Lng = (cm.north+cm.south)/2, (cm.east+cm.west)/2
	if m.Weather == nil {
		out.Reason = "no weather service"
		return out, nil
	}
	w, err := m.Weather.Current(ctx, out.Lat, out.Lng)
	if err != nil {
		out.Reason = "the weather service did not answer"
		return out, nil
	}
	return w, nil
}

func (m *Module) registerWeather(add func(tag string, rt route.Route)) {
	add("Course", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/courses/{id}/weather", Summary: "Live weather at the course (Open-Meteo, 10-minute cache)",
		Permission: "golf.course.view", Response: Weather{},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Weather, error) {
			cid, err := handle.ID(r)
			if err != nil {
				return Weather{}, err
			}
			return m.CourseWeather(ctx, tx, cid)
		})})
}
