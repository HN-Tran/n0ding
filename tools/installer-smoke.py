from __future__ import annotations

import hashlib
import http.server
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading


ROOT = Path(__file__).resolve().parents[1]


class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format: str, *args: object) -> None:
        pass

    def do_GET(self) -> None:
        if self.path == "/healthz":
            body = b'{"status":"ok"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        super().do_GET()


def write_assets(directory: Path) -> None:
    files = {
        "compose.yaml": "services:\n  n0ding:\n    image: ${N0DING_IMAGE}\n",
        "n0ding.toml": '[server]\nlisten = ":8080"\n',
    }
    for name, body in files.items():
        (directory / name).write_text(body, encoding="utf-8")
    checksums = "".join(
        f"{hashlib.sha256(body.encode()).hexdigest()}  {name}\n"
        for name, body in files.items()
    )
    (directory / "SHA256SUMS").write_text(checksums, encoding="utf-8")


def write_fake_docker(directory: Path, log: Path) -> None:
    if os.name == "nt":
        (directory / "docker.cmd").write_text(
            f"@echo %*>>\"{log}\"\r\n@exit /b 0\r\n", encoding="utf-8"
        )
    else:
        executable = directory / "docker"
        executable.write_text(
            f'#!/bin/sh\nprintf "%s\\n" "$*" >> "{log}"\nexit 0\n',
            encoding="utf-8",
        )
        executable.chmod(0o755)


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="n0ding-installer-") as temporary:
        base = Path(temporary)
        assets = base / "assets"
        binary = base / "bin"
        install = base / "install"
        docker_log = base / "docker.log"
        assets.mkdir()
        binary.mkdir()
        write_assets(assets)
        write_fake_docker(binary, docker_log)

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.RequestHandlerClass.directory = str(assets)
        # SimpleHTTPRequestHandler reads the directory from its constructor.
        handler = lambda *args, **kwargs: Handler(*args, directory=str(assets), **kwargs)
        server.RequestHandlerClass = handler  # type: ignore[assignment]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        port = server.server_address[1]

        env = os.environ.copy()
        env.update(
            {
                "PATH": str(binary) + os.pathsep + env["PATH"],
                "N0DING_VERSION": "v0.1.0",
                "N0DING_INSTALL_DIR": str(install),
                "N0DING_RELEASE_BASE_URL": f"http://127.0.0.1:{port}",
                "N0DING_HEALTH_URL": f"http://127.0.0.1:{port}/healthz",
            }
        )
        try:
            if os.name == "nt":
                command = [
                    "pwsh",
                    "-NoProfile",
                    "-File",
                    str(ROOT / "install.ps1"),
                    "-Version",
                    "v0.1.0",
                    "-InstallDir",
                    str(install),
                ]
            else:
                command = ["sh", str(ROOT / "install.sh")]
            subprocess.run(command, check=True, env=env)
        finally:
            server.shutdown()
            thread.join()

        for name in ("compose.yaml", "n0ding.toml", "SHA256SUMS", ".env"):
            assert (install / name).is_file(), name
        generated = (install / ".env").read_text(encoding="utf-8-sig")
        assert "N0DING_IMAGE=ghcr.io/hn-tran/n0ding:0.1.0" in generated
        calls = docker_log.read_text(encoding="utf-8")
        assert "compose" in calls and "pull" in calls and "up -d" in calls, calls


if __name__ == "__main__":
    main()
