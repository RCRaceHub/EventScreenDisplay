"""Entry point: python3 -m eventscreen [--config PATH]"""

from __future__ import annotations

import argparse
import logging
import signal
import sys
import threading
from pathlib import Path

from . import __version__, browsers, config
from .api import Agent, make_server
from .player import Player
from .playlist import Store
from .stage import Stage, StageBrowser


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="eventscreen", description="EventScreenDisplay viewer agent")
    parser.add_argument("--config", type=Path, default=config.DEFAULT_CONFIG_PATH)
    parser.add_argument("--browser", help="override the browser driver from the config")
    parser.add_argument("--port", type=int, help="override the API port from the config")
    parser.add_argument("--state-dir", type=Path, help="override the state directory from the config")
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args(argv)

    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.INFO,
                        format="%(levelname)s %(name)s: %(message)s")
    log = logging.getLogger("eventscreen")

    cfg = config.load(args.config)
    if args.browser:
        cfg.browser = args.browser
    if args.port:
        cfg.port = args.port
    if args.state_dir:
        cfg.state_dir = args.state_dir

    browser = browsers.create(cfg.browser, cfg.browser_args)
    base = f"http://127.0.0.1:{cfg.port}"
    stage = None
    if cfg.preload:
        stage = Stage()
        browser = StageBrowser(browser, stage, f"{base}/stage")
    player = Player(browser, Store(cfg.playlist_path), idle_url=f"{base}/idle",
                    preload_ahead=cfg.preload_ahead)
    agent = Agent(cfg, player, stage=stage)
    server = make_server(agent)

    stopping = threading.Event()

    def shutdown(signum, frame):
        if not stopping.is_set():
            stopping.set()
            threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)

    log.info("eventscreen %s (%s) listening on %s:%d, browser=%s",
             __version__, cfg.screen_id, cfg.listen, cfg.port, cfg.browser)
    player.start()
    try:
        server.serve_forever()
    finally:
        player.stop()
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
