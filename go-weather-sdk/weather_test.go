package weather_test

import (
	"testing"
	"time"

	weather "github.com/gopkg-utils/go-weather-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := weather.NewClient("")
	require.NotNil(t, c)
}

func TestNewClientWithOptions(t *testing.T) {
	c := weather.NewClient(
		"test-key",
		weather.WithBaseURL("https://custom.api.example.com"),
		weather.WithTimeout(5*time.Second),
		weather.WithCacheTTL(30*time.Second),
	)
	require.NotNil(t, c)
}

func TestCurrent(t *testing.T) {
	c := weather.NewClient("")
	cond, err := c.Current("Paris")
	require.NoError(t, err)
	assert.Equal(t, "Paris", cond.Location)
	assert.NotZero(t, cond.TempC)
	assert.NotZero(t, cond.Humidity)
	assert.False(t, cond.UpdatedAt.IsZero())
}

func TestCurrentCached(t *testing.T) {
	c := weather.NewClient("", weather.WithCacheTTL(time.Minute))
	cond1, err := c.Current("Tokyo")
	require.NoError(t, err)

	cond2, err := c.Current("Tokyo")
	require.NoError(t, err)

	assert.Equal(t, cond1.UpdatedAt, cond2.UpdatedAt, "second call should return cached result")
}

func TestForecast(t *testing.T) {
	c := weather.NewClient("")
	fc, err := c.Forecast("London", 5)
	require.NoError(t, err)
	assert.Len(t, fc, 5)
	for i, day := range fc {
		assert.True(t, day.Date.After(time.Now().Add(-24*time.Hour)),
			"day %d date should be in the future", i)
		assert.GreaterOrEqual(t, day.HighC, day.LowC,
			"high should be >= low for day %d", i)
	}
}

func TestForecastValidation(t *testing.T) {
	c := weather.NewClient("")

	_, err := c.Forecast("NYC", 0)
	assert.Error(t, err, "days=0 should error")

	_, err = c.Forecast("NYC", 17)
	assert.Error(t, err, "days=17 should error")

	_, err = c.Forecast("NYC", 1)
	assert.NoError(t, err, "days=1 should be valid")

	_, err = c.Forecast("NYC", 16)
	assert.NoError(t, err, "days=16 should be valid")
}
