#!/usr/bin/env python3
"""Takes the README's screenshots from the real app and a real agent.

    python3 scripts/take-screenshots.py [--device "iPhone 17 Pro"] [--dark]
    python3 scripts/take-screenshots.py --web [--dark]

It starts a demo agent with its own configuration, port and sample project
and runs Forge against it: in the iOS simulator, or with --web in a browser
window the size of a desktop. Nothing of your own agent or app is touched.

The web client only connects over TLS, so --web opens a temporary Cloudflare
tunnel to the demo agent for the length of the run. The agent still accepts
only the demo device.

The demo agent uses Claude Code, so `claude` must be installed and signed in,
and the run uses that account. The app driver rejects requests for approval,
except changes to files of the sample project; see
flutter/pi_go_app/tool/screenshots/main.dart.

Needs macOS with Xcode, Flutter and Go; --web needs Chrome and cloudflared.
"""
import argparse
import functools
import http.server
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent
APP = ROOT / "flutter" / "pi_go_app"
PORT = 17346
ENDPOINT = f"ws://127.0.0.1:{PORT}/ws"

SAMPLE = {
    "go.mod": "module example.com/textstats\n\ngo 1.26\n",
    "stats.go": (
        "// Package textstats reports simple statistics about a text.\n"
        "package textstats\n\n"
        'import "strings"\n\n'
        "// WordCount returns the number of words in text.\n"
        "func WordCount(text string) int {\n"
        "\treturn len(strings.Fields(text))\n"
        "}\n"
    ),
    "stats_test.go": (
        "package textstats\n\n"
        'import "testing"\n\n'
        "func TestWordCount(t *testing.T) {\n"
        '\tif got := WordCount("the quick brown fox"); got != 4 {\n'
        '\t\tt.Fatalf("WordCount = %d, want 4", got)\n'
        "\t}\n"
        "}\n"
    ),
    "README.md": "# textstats\n\nSmall helpers that report statistics about a text.\n",
    "notes.txt": "scratch notes from last week\n",
}


def run(*command, **options):
    return subprocess.run(command, check=True, **options)


class Agent:
    def __init__(self, home, model, classifier):
        self.home = home
        self.config = home / "config"
        self.workspace = home / "workspace"
        self.binary = home / "pi-go-agent"
        self.model = model
        self.process = None
        self.config.mkdir(parents=True)
        self.config.chmod(0o700)
        self.write("authorized-devices.json", {"version": 1, "devices": []})
        self.write("classifier.json", {"version": 1, "model": classifier})
        self.workspace.mkdir()
        for name, text in SAMPLE.items():
            (self.workspace / name).write_text(text)
        git = ["git", "-C", str(self.workspace), "-c", "user.name=demo", "-c", "user.email=demo@example.com"]
        run(*git, "init", "-q")
        run(*git, "add", "-A")
        run(*git, "commit", "-q", "-m", "initial import")
        run("go", "build", "-o", str(self.binary), "./cmd/pi-go-agent", cwd=ROOT)

    def write(self, name, value):
        path = self.config / name
        path.write_text(json.dumps(value, indent=2))
        path.chmod(0o600)

    def allow(self, device):
        self.write("authorized-devices.json", {"version": 1, "devices": [device]})
        self.start()

    def start(self):
        self.stop()
        environment = dict(os.environ, PI_GO_CONFIG_DIR=str(self.config), PI_GO_PUSH_DISABLED="true")
        self.process = subprocess.Popen(
            [str(self.binary), "--listen", ENDPOINT, "--cwd", str(self.workspace), "--model", self.model],
            env=environment,
            stdout=open(self.home / "agent.log", "a"),
            stderr=subprocess.STDOUT,
        )
        time.sleep(1.5)
        if self.process.poll() is not None:
            sys.exit(f"the demo agent did not start; see {self.home / 'agent.log'}")

    def stop(self):
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
        self.process = None


def simulator(name):
    devices = json.loads(
        subprocess.check_output(["xcrun", "simctl", "list", "devices", "available", "-j"])
    )["devices"]
    for runtime in sorted(devices, reverse=True):
        for device in devices[runtime]:
            if device["name"] == name:
                return device["udid"]
    sys.exit(f"no simulator named {name}")


def tunnel():
    """Opens a temporary tunnel to the demo agent and returns its address."""
    process = subprocess.Popen(
        ["cloudflared", "tunnel", "--no-autoupdate", "--url", f"http://127.0.0.1:{PORT}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    deadline = time.time() + 60
    for line in process.stderr:
        found = re.search(r"https://([a-z0-9-]+\.trycloudflare\.com)", line)
        if found:
            # Keep reading so that the tunnel never blocks on a full pipe.
            threading.Thread(target=lambda: [None for _ in process.stderr], daemon=True).start()
            return process, f"wss://{found.group(1)}/ws"
        if time.time() > deadline:
            break
    process.terminate()
    sys.exit("the tunnel did not start")


def defines(agent, report="", endpoint=ENDPOINT):
    return [
        "--dart-define=FORGE_PUSH_RELAY_URL=",
        f"--dart-define=DEMO_ENDPOINT={endpoint}",
        f"--dart-define=DEMO_WORKSPACE={agent.workspace}",
        f"--dart-define=DEMO_REPORT={report}",
    ]


def in_browser(agent, out, dark):
    """Runs the web client in a headless browser and saves what it reports."""
    port = PORT + 1
    origin = f"http://127.0.0.1:{port}"
    passage, endpoint = tunnel()
    print("tunnel", endpoint, flush=True)
    run(
        "flutter", "build", "web", "-t", "tool/screenshots/main.dart",
        *defines(agent, origin, endpoint), cwd=APP,
    )
    site = APP / "build" / "web"

    finished = threading.Event()
    result = {"status": 1}

    class Handler(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *arguments):
            pass

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            if self.path.startswith("/shot/"):
                name = pathlib.PurePosixPath(self.path).name
                (out / f"{name}.png").write_bytes(body)
                print("shot", name, flush=True)
            else:
                kind, _, detail = body.decode().partition(" ")
                print(kind, detail[:100], flush=True)
                if kind == "identity":
                    agent.allow(json.loads(detail))
                elif kind in ("done", "failed"):
                    result["status"] = 0 if kind == "done" else 1
                    finished.set()
            self.send_response(204)
            self.end_headers()

    server = http.server.ThreadingHTTPServer(
        ("127.0.0.1", port), functools.partial(Handler, directory=str(site))
    )
    threading.Thread(target=server.serve_forever, daemon=True).start()

    chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    with tempfile.TemporaryDirectory() as profile:
        browser = subprocess.Popen(
            [
                chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars",
                f"--user-data-dir={profile}", "--window-size=1280,800",
                "--force-device-scale-factor=2",
                "--force-dark-mode" if dark else "--disable-features=DarkMode",
                "--remote-debugging-port=0", origin,
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        try:
            finished.wait(timeout=900)
        finally:
            browser.terminate()
            browser.wait(timeout=10)
            server.shutdown()
            passage.terminate()
    return result["status"]


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--device", default="iPhone 17 Pro")
    parser.add_argument("--dark", action="store_true")
    parser.add_argument("--web", action="store_true")
    parser.add_argument("--model", default="claude/claude-sonnet-5")
    parser.add_argument("--classifier", default="claude/claude-sonnet-5")
    parser.add_argument("--out", default=str(ROOT / "dist" / "screenshots"))
    # /tmp is a symlink on macOS, and the safety gate asks about every path
    # that crosses one.
    parser.add_argument("--home", default="/private/tmp/forge-demo")
    options = parser.parse_args()

    out = pathlib.Path(options.out) / (
        ("desktop" if options.web else "phone") + ("-dark" if options.dark else "")
    )
    out.mkdir(parents=True, exist_ok=True)
    home = pathlib.Path(options.home)
    shutil.rmtree(home, ignore_errors=True)
    home.mkdir(parents=True)

    agent = Agent(home, options.model, options.classifier)
    agent.start()

    if options.web:
        try:
            status = in_browser(agent, out, options.dark)
        finally:
            agent.stop()
        print("screenshots in", out)
        sys.exit(status)

    udid = simulator(options.device)
    subprocess.run(["xcrun", "simctl", "boot", udid], stderr=subprocess.DEVNULL)
    run("xcrun", "simctl", "bootstatus", udid, stdout=subprocess.DEVNULL)
    run("xcrun", "simctl", "ui", udid, "appearance", "dark" if options.dark else "light")
    run(
        "xcrun", "simctl", "status_bar", udid, "override",
        "--time", "9:41", "--batteryState", "charged", "--batteryLevel", "100",
        "--cellularBars", "4", "--wifiBars", "3", "--dataNetwork", "wifi",
    )
    # A fresh install has a fresh identity and no remembered servers.
    subprocess.run(["xcrun", "simctl", "uninstall", udid, "com.tingouw.forge"], stderr=subprocess.DEVNULL)

    log = open(home / "app.log", "w")
    flutter = subprocess.Popen(
        [
            "flutter", "run", "-d", udid, "-t", "tool/screenshots/main.dart",
            *defines(agent),
        ],
        cwd=APP,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        stdin=subprocess.DEVNULL,
        text=True,
        start_new_session=True,
    )
    status = 1
    try:
        for line in flutter.stdout:
            log.write(line)
            log.flush()
            found = re.search(r"FORGE-DEMO (\w+) ?(.*)", line)
            if not found:
                continue
            kind, detail = found.group(1), found.group(2).strip()
            print(kind, detail[:100], flush=True)
            if kind == "identity":
                agent.allow(json.loads(detail))
            elif kind == "shot":
                run(
                    "xcrun", "simctl", "io", udid, "screenshot", str(out / f"{detail}.png"),
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                )
            elif kind == "done":
                status = 0
                break
            elif kind == "failed":
                break
    finally:
        os.killpg(flutter.pid, signal.SIGTERM)
        agent.stop()
        subprocess.run(["xcrun", "simctl", "status_bar", udid, "clear"], stderr=subprocess.DEVNULL)
        subprocess.run(["xcrun", "simctl", "shutdown", udid], stderr=subprocess.DEVNULL)
    print("screenshots in", out)
    sys.exit(status)


if __name__ == "__main__":
    main()
