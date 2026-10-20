import os
import pty
import socket
import subprocess
import time
from collections.abc import Callable
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]


@pytest.fixture(scope="session")
def binary(tmp_path_factory: pytest.TempPathFactory) -> str:
    """The nodloop of this checkout built once for the session"""
    out = tmp_path_factory.mktemp("bin") / "nodloop"
    subprocess.run(["go", "build", "-o", str(out), "./cmd/nodloop"], cwd=ROOT, check=True)
    return str(out)


@pytest.fixture(scope="session")
def server_binary(tmp_path_factory: pytest.TempPathFactory) -> str:
    """The nodloop-server of this checkout built once for the session"""
    out = tmp_path_factory.mktemp("bin") / "nodloop-server"
    subprocess.run(["go", "build", "-o", str(out), "./cmd/nodloop-server"], cwd=ROOT / "server", check=True)
    return str(out)


@pytest.fixture(autouse=True)
def home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """A home of the test so an approval never writes a veto file under the real one"""
    path = tmp_path / "home"
    path.mkdir(exist_ok=True)
    monkeypatch.setenv("HOME", str(path))
    return path


@pytest.fixture
def records(tmp_path: Path) -> str:
    return str(tmp_path / "records")


@pytest.fixture
def approve(binary: str, records: str) -> Callable[..., None]:
    """Approves a version of the records as the person ann at a terminal of their own

    An SDK call proves no person so it records a proposal
    The CLI decides only when stdin and stdout are a terminal outside an agent session
    A pseudo terminal stands for the person here
    """

    def run(item_id: str, version: int, *flags: str) -> None:
        env = {k: v for k, v in os.environ.items() if k != "CLAUDECODE"}
        args = [binary, "knowledge", "approve", item_id, "--version", str(version), "--approver", "ann", "--record-dir", records, *flags]
        leader, follower = pty.openpty()
        try:
            done = subprocess.run(args, stdin=follower, stdout=follower, stderr=subprocess.PIPE, env=env, timeout=60, check=False)
        finally:
            os.close(follower)
        try:
            os.read(leader, 65536)
        except OSError:
            pass
        os.close(leader)
        assert done.returncode == 0, done.stderr.decode()

    return run


@pytest.fixture
def server(server_binary: str, tmp_path: Path):
    """A nodloop server on a free port yielding its url, an approver key of tenant acme a person holds
    and an approver key of a service that proves no person"""
    env = dict(os.environ)
    add = [server_binary, "key", "add", "ann", "--tenant", "acme", "--role", "approver", "--person"]
    key = subprocess.run(add, env=env, check=True, capture_output=True, text=True).stdout.strip()
    add = [server_binary, "key", "add", "ci", "--tenant", "acme", "--role", "approver"]
    service = subprocess.run(add, env=env, check=True, capture_output=True, text=True).stdout.strip()
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    serve = [server_binary, "serve", "--addr", f"127.0.0.1:{port}", "--record-dir", str(tmp_path / "records")]
    proc = subprocess.Popen(serve, env=env, stderr=subprocess.DEVNULL)
    for _ in range(100):
        try:
            socket.create_connection(("127.0.0.1", port), timeout=0.1).close()
            break
        except OSError:
            time.sleep(0.05)
    yield f"http://127.0.0.1:{port}/mcp", key, service
    proc.terminate()
    proc.wait()
