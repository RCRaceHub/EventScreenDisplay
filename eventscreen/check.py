"""On-device health check: is this screen set up and working?

    sudo eventscreen-check            # or: python3 -m eventscreen.check
    eventscreen-check --json

Prints one PASS / WARN / FAIL line per check and exits 1 if anything
failed, so it also works in scripts. Each check is a small function that
takes a Probe, which wraps everything that touches the system so the
checks can be tested without a Pi.
"""

from __future__ import annotations

import argparse
import glob
import json
import shutil
import subprocess
import sys
import urllib.request
from dataclasses import asdict, dataclass
from pathlib import Path

from . import __version__, avahi, config

PASS, WARN, FAIL = "PASS", "WARN", "FAIL"


@dataclass
class Result:
    name: str
    level: str
    detail: str
    fix: str = ""


class Probe:
    """Reads the system. Tests swap in a fake."""

    def __init__(self, cfg: config.Config):
        self.cfg = cfg

    def run(self, *cmd: str) -> tuple[int, str]:
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
            return r.returncode, (r.stdout or r.stderr).strip()
        except (OSError, subprocess.TimeoutExpired) as e:
            return 127, str(e)

    def which(self, binary: str) -> str | None:
        return shutil.which(binary)

    def read(self, path: str) -> str | None:
        try:
            return Path(path).read_text()
        except OSError:
            return None

    def glob(self, pattern: str) -> list[str]:
        return sorted(glob.glob(pattern))

    def api_status(self) -> dict:
        url = f"http://127.0.0.1:{self.cfg.port}/api/v1/status"
        with urllib.request.urlopen(url, timeout=5) as r:
            return json.loads(r.read())


def check_services(p: Probe) -> list[Result]:
    out = []
    for unit, required in (("eventscreen", True), ("avahi-daemon", True),
                           ("eventscreen-wifi", p.cfg.wifi_fallback)):
        _, state = p.run("systemctl", "is-active", unit)
        state = state.splitlines()[0] if state else ""
        if state == "active":
            out.append(Result(f"service {unit}", PASS, "running"))
        elif unit == "eventscreen-wifi" and state in ("inactive", ""):
            # It exits cleanly when there's no Wi-Fi to look after.
            out.append(Result(f"service {unit}", WARN, f"not running ({state or 'unknown'})",
                              "fine on Ethernet-only Pis; else journalctl -u eventscreen-wifi"))
        else:
            out.append(Result(f"service {unit}", FAIL if required else WARN,
                              f"not running ({state or 'unknown'})",
                              f"journalctl -u {unit} -e"))
    return out


def check_browser(p: Probe) -> list[Result]:
    need = {"cog": ["cog", "cogctl"], "chromium": ["cage"], "dummy": []}.get(p.cfg.browser)
    if need is None:
        return [Result("browser", FAIL, f"unknown browser {p.cfg.browser!r} in config")]
    missing = [b for b in need if not p.which(b)]
    if p.cfg.browser == "chromium" and not (p.which("chromium-browser") or p.which("chromium")):
        missing.append("chromium-browser")
    # WebKit's page renderer needs OpenGL ES even though nothing links cog to
    # it directly; without it every page crashes ("renderer process crashed").
    if p.cfg.browser == "cog" and not (p.glob("/usr/lib/*/libGLESv2.so.2*") or p.glob("/usr/lib/libGLESv2.so.2*")):
        missing.append("libgles2")
    if missing:
        return [Result("browser", FAIL, f"{p.cfg.browser}: missing {', '.join(missing)}",
                       f"sudo eventscreen-setup --browser {p.cfg.browser}")]
    return [Result("browser", PASS, f"{p.cfg.browser} installed")]


def check_hdmi(p: Probe) -> list[Result]:
    out = []
    ports = p.glob("/sys/class/drm/card*-HDMI-A-*/status")
    if not ports:
        out.append(Result("HDMI", WARN, "no HDMI output found (not a Pi, or no KMS driver)"))
    else:
        states = {Path(s).parent.name.split("-", 1)[1]: (p.read(s) or "").strip() for s in ports}
        connected = [k for k, v in states.items() if v == "connected"]
        if connected:
            out.append(Result("HDMI", PASS, f"{', '.join(connected)} connected"))
        else:
            out.append(Result("HDMI", FAIL, "nothing connected: " +
                              ", ".join(f"{k} {v}" for k, v in states.items()),
                              "check the cable, adapter and TV input; force it on with "
                              "sudo eventscreen-setup --resolution 1920x1080@60D"))
    cmdline = p.read("/boot/firmware/cmdline.txt") or p.read("/boot/cmdline.txt")
    if cmdline is None:
        out.append(Result("display mode", WARN, "no cmdline.txt (not a Raspberry Pi?)"))
    else:
        mode = next((w.split(":", 1)[1] for w in cmdline.split() if w.startswith("video=HDMI-A-1:")), "")
        if not mode:
            out.append(Result("display mode", WARN, "HDMI mode not pinned; the TV picks it",
                              "sudo eventscreen-setup && sudo reboot"))
        elif "D" not in mode.split(",")[0]:
            out.append(Result("display mode", WARN, f"{mode}, not forced on",
                              "sudo eventscreen-setup --resolution 1920x1080@60D && sudo reboot"))
        else:
            out.append(Result("display mode", PASS, mode))
    return out


def check_api(p: Probe) -> list[Result]:
    try:
        st = p.api_status()
    except Exception as e:
        return [Result("API", FAIL, f"no answer on port {p.cfg.port}: {e}",
                       "journalctl -u eventscreen -e")]
    out = [Result("API", PASS, f"answering on port {p.cfg.port}, state {st.get('state')}")]
    b = st.get("browser") or {}
    if b.get("running"):
        out.append(Result("browser running", PASS, f"pid {b.get('pid')}, {b.get('restarts', 0)} restarts"))
    else:
        out.append(Result("browser running", FAIL, b.get("last_error") or "not running",
                          "journalctl -u eventscreen -e"))
    reachable = (st.get("network") or {}).get("page_reachable")
    current = (st.get("current") or {}).get("url", "")
    if reachable is False:
        out.append(Result("page server", FAIL, f"can't reach {current}",
                          "check the timing server is up and on this network"))
    elif reachable:
        out.append(Result("page server", PASS, f"{current} reachable"))
    return out


def check_network(p: Probe) -> list[Result]:
    code, out = p.run("nmcli", "-t", "-f", "TYPE,STATE,CONNECTION", "device")
    if code != 0:
        return [Result("network", WARN, f"can't ask NetworkManager: {out}")]
    rows = [line.split(":", 2) for line in out.splitlines()]
    hotspot = any(len(r) == 3 and r[2] == "eventscreen-hotspot" and r[1] == "connected" for r in rows)
    up = [r[2] for r in rows if len(r) == 3 and r[0] in ("wifi", "ethernet")
          and r[1] == "connected" and r[2] != "eventscreen-hotspot"]
    if up:
        res = [Result("network", PASS, f"connected ({', '.join(up)})")]
    elif hotspot:
        res = [Result("network", FAIL, "in Wi-Fi setup mode (hotspot up)",
                      "join the hotspot shown on the TV and pick a network")]
    else:
        res = [Result("network", FAIL, "not connected", "nmcli device wifi list")]
    if p.read(str(avahi.SERVICE_PATH)) is not None:
        res.append(Result("mDNS", PASS, f"advertising as {_hostname(p)}.local"))
    else:
        res.append(Result("mDNS", FAIL, f"{avahi.SERVICE_PATH} missing", "sudo systemctl restart eventscreen"))
    return res


def check_power(p: Probe) -> list[Result]:
    code, out = p.run("vcgencmd", "get_throttled")
    if code != 0:
        return []
    value = out.split("=", 1)[-1]
    if value == "0x0":
        return [Result("power", PASS, "no under-voltage or throttling")]
    return [Result("power", WARN, f"throttled={value}: under-voltage or overheating since boot",
                   "use the official power supply and a short cable")]


def _hostname(p: Probe) -> str:
    return (p.read("/etc/hostname") or "").strip() or "?"


CHECKS = [check_services, check_browser, check_hdmi, check_api, check_network, check_power]


def run_checks(p: Probe) -> list[Result]:
    results = []
    for check in CHECKS:
        try:
            results.extend(check(p))
        except Exception as e:  # a broken check shouldn't hide the others
            results.append(Result(check.__name__.removeprefix("check_"), WARN, f"check failed: {e}"))
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="eventscreen-check", description="Check this screen is working")
    parser.add_argument("--config", type=Path, default=config.DEFAULT_CONFIG_PATH)
    parser.add_argument("--json", action="store_true", help="print results as JSON")
    args = parser.parse_args(argv)
    try:
        cfg = config.load(args.config)
        cfg_result = Result("config", PASS, str(args.config))
    except Exception as e:
        cfg = config.Config()
        cfg_result = Result("config", FAIL, f"{args.config}: {e}", f"fix {args.config}")
    results = [cfg_result, *run_checks(Probe(cfg))]
    failed = any(r.level == FAIL for r in results)
    if args.json:
        print(json.dumps({"version": __version__, "ok": not failed,
                          "results": [asdict(r) for r in results]}, indent=2))
    else:
        print(f"eventscreen {__version__} on {_hostname(Probe(cfg))}")
        for r in results:
            print(f"  {r.level}  {r.name}: {r.detail}")
            if r.fix and r.level != PASS:
                print(f"        -> {r.fix}")
        print("All good." if not failed else "Some checks failed.")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
