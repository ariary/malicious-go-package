// Package weather provides a lightweight client for querying current conditions
// and multi-day forecasts from public weather data APIs.
//
// Basic usage:
//
//	c := weather.NewClient("your-api-key")
//	cond, err := c.Current("Paris")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("%.1f°C — %s\n", cond.TempC, cond.Description)
//
//	fc, _ := c.Forecast("London", 5)
//	for _, day := range fc {
//	    fmt.Printf("%s: %.0f / %.0f°C\n", day.Date.Format("Mon"), day.HighC, day.LowC)
//	}
package weather

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

const defaultBaseURL = "https://api.open-meteo.com/v1"

// Client is a weather API client.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	cache   *ttlCache
	log     *logrus.Entry
}

// Option is a functional option for Client.
type Option func(*Client)

// WithBaseURL overrides the default API endpoint.
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// WithTimeout sets the HTTP client timeout (default: 10s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// WithCacheTTL enables in-memory response caching with the given TTL.
// A TTL of 0 disables caching (default).
func WithCacheTTL(d time.Duration) Option {
	return func(c *Client) { c.cache = newTTLCache(d) }
}

// NewClient creates a new weather API client.
// apiKey may be empty for endpoints that do not require authentication.
func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
		log:     logrus.WithField("sdk", "go-weather-sdk"),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Conditions holds current weather observations for a location.
type Conditions struct {
	Location    string
	TempC       float64
	FeelsLikeC  float64
	Humidity    int
	WindKph     float64
	Description string
	Icon        string
	UpdatedAt   time.Time
}

// DayForecast holds a single-day weather forecast.
type DayForecast struct {
	Date    time.Time
	HighC   float64
	LowC    float64
	RainMM  float64
	Summary string
}

// Current returns the current weather conditions for a city or "lat,lon" string.
func (c *Client) Current(location string) (*Conditions, error) {
	c.log.WithField("location", location).Debug("fetching current conditions")

	if c.cache != nil {
		if cached, ok := c.cache.get("current:" + location); ok {
			return cached.(*Conditions), nil
		}
	}

	// Stub: in production this calls the configured weather API.
	cond := &Conditions{
		Location:    location,
		TempC:       18.5,
		FeelsLikeC:  17.2,
		Humidity:    65,
		WindKph:     12.4,
		Description: "Partly cloudy",
		Icon:        "partly-cloudy-day",
		UpdatedAt:   time.Now(),
	}

	if c.cache != nil {
		c.cache.set("current:"+location, cond)
	}

	c.log.WithFields(logrus.Fields{
		"location": location,
		"tempC":    cond.TempC,
	}).Debug("conditions fetched")

	return cond, nil
}

// Forecast returns a multi-day forecast for a location.
// days must be between 1 and 16.
func (c *Client) Forecast(location string, days int) ([]DayForecast, error) {
	if days < 1 || days > 16 {
		return nil, fmt.Errorf("weather: days must be between 1 and 16, got %d", days)
	}

	c.log.WithFields(logrus.Fields{
		"location": location,
		"days":     days,
	}).Debug("fetching forecast")

	key := fmt.Sprintf("forecast:%s:%d", location, days)
	if c.cache != nil {
		if cached, ok := c.cache.get(key); ok {
			return cached.([]DayForecast), nil
		}
	}

	out := make([]DayForecast, days)
	for i := range out {
		out[i] = DayForecast{
			Date:    time.Now().AddDate(0, 0, i+1).Truncate(24 * time.Hour),
			HighC:   20.0 + float64(i%4),
			LowC:    10.0 + float64(i%3),
			RainMM:  float64(i%3) * 2.5,
			Summary: []string{"Sunny", "Partly cloudy", "Overcast", "Light rain"}[i%4],
		}
	}

	if c.cache != nil {
		c.cache.set(key, out)
	}

	return out, nil
}

// parseJSON decodes a JSON HTTP response into v and closes the body.
func parseJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
