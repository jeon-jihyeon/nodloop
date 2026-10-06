import os
import socket
import subprocess
import time
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]


@pytest.fixture(scope="session")
def binary(tmp_path_factory: pytest.TempPathFactory) -> str:
    """The nodloop of this checkout built once for the session"""
    out = tmp_path_factory.mktemp("bin") / "nodloop"
    subprocess.run(["go", "build", "-o", str(out), "./cmd/nodloop"], cwd=ROOT, check=True)
    return str(out)


@pytest.fixture
def records(tmp_path: Path) -> str:
    return str(tmp_path / "records")


@pytest.fixture
def server(binary: str, tmp_path: Path):
    """A nodloop server on a free port with an approver key of tenant acme, yielding its url and key"""
    home = tmp_path / "home"
    home.mkdir()
    env = {**os.environ, "HOME": str(home)}
    add = [binary, "server", "key", "add", "ann", "--tenant", "acme", "--role", "approver"]
    key = subprocess.run(add, env=env, check=True, capture_output=True, text=True).stdout.strip()
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    serve = [binary, "server", "serve", "--addr", f"127.0.0.1:{port}", "--record-dir", str(tmp_path / "records")]
    proc = subprocess.Popen(serve, env=env, stderr=subprocess.DEVNULL)
    for _ in range(100):
        try:
            socket.create_connection(("127.0.0.1", port), timeout=0.1).close()
            break
        except OSError:
            time.sleep(0.05)
    yield f"http://127.0.0.1:{port}/mcp", key
    proc.terminate()
    proc.wait()
