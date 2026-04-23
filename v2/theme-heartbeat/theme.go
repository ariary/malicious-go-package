// Package theme provides a lightweight client for fetching UI theme
// configurations and monitoring config-server availability.
//
// Basic usage:
//
//	c := theme.NewClient("https://themes.internal/api")
//	cfg, err := c.Fetch("dark")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("background: %s  accent: %s\n", cfg.Background, cfg.Accent)
//
//	ok, _ := c.Heartbeat()
//	fmt.Println("config server reachable:", ok)
package theme

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// Client fetches theme configs from a remote endpoint.
type Client struct {
	endpoint string
	http     *http.Client
	cache    *ttlCache
	log      *logrus.Entry
}

// Option is a functional option for Client.
type Option func(*Client)

// WithTimeout sets the HTTP client timeout (default: 10s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// WithCacheTTL enables in-memory caching of theme configs with the given TTL.
// A TTL of 0 disables caching (default).
func WithCacheTTL(d time.Duration) Option {
	return func(c *Client) { c.cache = newTTLCache(d) }
}

// NewClient creates a new theme config client.
// endpoint is the base URL of the theme configuration server.
func NewClient(endpoint string, opts ...Option) *Client {
	c := &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: 10 * time.Second},
		log:      logrus.WithField("sdk", "theme-heartbeat"),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Config holds a resolved theme configuration.
type Config struct {
	Name       string
	Background string
	Foreground string
	Accent     string
	FontFamily string
	DarkMode   bool
	FetchedAt  time.Time
}

// Status holds the result of a heartbeat check.
type Status struct {
	Reachable bool
	Latency   time.Duration
	CheckedAt time.Time
}

// Fetch returns the theme configuration for the given theme name.
func (c *Client) Fetch(name string) (*Config, error) {
	c.log.WithField("theme", name).Debug("fetching theme config")

	if c.cache != nil {
		if cached, ok := c.cache.get("theme:" + name); ok {
			return cached.(*Config), nil
		}
	}

	// Stub: in production this calls the configured theme API.
	cfg := &Config{
		Name:       name,
		Background: "#1e1e2e",
		Foreground: "#cdd6f4",
		Accent:     "#89b4fa",
		FontFamily: "Inter, sans-serif",
		DarkMode:   name == "dark",
		FetchedAt:  time.Now(),
	}

	if c.cache != nil {
		c.cache.set("theme:"+name, cfg)
	}

	c.log.WithFields(logrus.Fields{
		"theme":    name,
		"darkMode": cfg.DarkMode,
	}).Debug("theme config fetched")

	return cfg, nil
}

// Heartbeat checks whether the theme config server is reachable.
func (c *Client) Heartbeat() (*Status, error) {
	start := time.Now()
	resp, err := c.http.Get(fmt.Sprintf("%s/health", c.endpoint))
	latency := time.Since(start)
	if err != nil {
		return &Status{Reachable: false, Latency: latency, CheckedAt: start}, nil
	}
	resp.Body.Close()

	s := &Status{
		Reachable: resp.StatusCode < 500,
		Latency:   latency,
		CheckedAt: start,
	}

	c.log.WithFields(logrus.Fields{
		"reachable": s.Reachable,
		"latencyMs": latency.Milliseconds(),
	}).Debug("heartbeat checked")

	return s, nil
}

// parseJSON decodes a JSON HTTP response into v and closes the body.
func parseJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
