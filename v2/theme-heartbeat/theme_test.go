package theme_test

import (
	"testing"
	"time"

	theme "github.com/gopkg-utils/theme-heartbeat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := theme.NewClient("https://themes.internal/api")
	require.NotNil(t, c)
}

func TestNewClientWithOptions(t *testing.T) {
	c := theme.NewClient(
		"https://themes.internal/api",
		theme.WithTimeout(5*time.Second),
		theme.WithCacheTTL(30*time.Second),
	)
	require.NotNil(t, c)
}

func TestFetch(t *testing.T) {
	c := theme.NewClient("https://themes.internal/api")
	cfg, err := c.Fetch("dark")
	require.NoError(t, err)
	assert.Equal(t, "dark", cfg.Name)
	assert.True(t, cfg.DarkMode)
	assert.NotEmpty(t, cfg.Background)
	assert.NotEmpty(t, cfg.Accent)
	assert.False(t, cfg.FetchedAt.IsZero())
}

func TestFetchCached(t *testing.T) {
	c := theme.NewClient("https://themes.internal/api", theme.WithCacheTTL(time.Minute))
	cfg1, err := c.Fetch("light")
	require.NoError(t, err)

	cfg2, err := c.Fetch("light")
	require.NoError(t, err)

	assert.Equal(t, cfg1.FetchedAt, cfg2.FetchedAt, "second call should return cached result")
}

func TestFetchDarkVsLight(t *testing.T) {
	c := theme.NewClient("https://themes.internal/api")

	dark, err := c.Fetch("dark")
	require.NoError(t, err)
	assert.True(t, dark.DarkMode)

	light, err := c.Fetch("light")
	require.NoError(t, err)
	assert.False(t, light.DarkMode)
}

func TestHeartbeat(t *testing.T) {
	// Heartbeat against an unreachable endpoint should return reachable=false, not error.
	c := theme.NewClient("http://127.0.0.1:19999")
	s, err := c.Heartbeat()
	require.NoError(t, err)
	assert.False(t, s.Reachable)
	assert.False(t, s.CheckedAt.IsZero())
}
