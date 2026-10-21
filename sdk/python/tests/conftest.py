import os
import pty
import socket
import subprocess
import time
from collections.abc import Callable
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]

# Prefixes of the variables a shell may set that change what nodloop or the SDK reads and writes
# 1. NODLOOP_ names the record dir, the binary, the session mode, the plugin version and the database of the server
# 2. CLAUDE names an agent session, which turns a decision at a terminal into a proposal, and the config dir of the host
# 3. OTEL_ names a target of the export
SHELL_PREFIXES = ("NODLOOP_", "CLAUDE", "OTEL_")


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
    """A home and an environment of the test so neither the real ~/.nodloop nor a variable of the shell running the tests is read or written

    The binaries are built before it with the real home since go keeps its caches there
    """
    for name in list(os.environ):
        if name.startswith(SHELL_PREFIXES):
            monkeypatch.delenv(name)
    path = tmp_path / "home"
    path.mkdir(exist_ok=True)
    monkeypatch.setenv("HOME", str(path))
    return path


@pytest.fixture
def records(tmp_path: Path) -> str:
    return str(tmp_path / "records")


def at_terminal(args: list[str]) -> str:
    """Runs the command as a person at a terminal of their own and answers what it printed

    A command that decides for a person or adds a key that proves one runs only when stdin and stdout are a terminal
    outside an agent session
    A pseudo terminal stands for the person here and the environment of the test names no agent session
    """
    leader, follower = pty.openpty()
    agent_variables = {"CLAUDECODE", "CODEX_THREAD_ID", "CURSOR_AGENT", "COPILOT_AGENT_SESSION_ID"}
    env = {name: value for name, value in os.environ.items() if name not in agent_variables}
    # The output is read while the follower is open since macOS drops what a closed pseudo terminal held
    os.set_blocking(leader, False)
    try:
        done = subprocess.run(args, stdin=follower, stdout=follower, stderr=subprocess.PIPE, timeout=60, env=env, check=False)
        try:
            printed = os.read(leader, 65536).decode()
        except BlockingIOError:
            printed = ""
    finally:
        os.close(follower)
        os.close(leader)
    assert done.returncode == 0, done.stderr.decode()
    return printed.strip()


@pytest.fixture
def approve(binary: str, records: str) -> Callable[..., None]:
    """Approves a version of the records as the person ann at a terminal of their own since an SDK call records only a proposal"""

    def run(item_id: str, version: int, *flags: str) -> None:
        args = [binary, "knowledge", "approve", item_id, "--version", str(version), "--approver", "ann", "--record-dir", records, *flags]
        at_terminal(args)

    return run


@pytest.fixture
def server(server_binary: str, tmp_path: Path):
    """A nodloop server on a free port yielding its url, an approver key of tenant acme a person holds
    and an approver key of a service that proves no person"""
    key = at_terminal([server_binary, "key", "add", "ann", "--tenant", "acme", "--role", "approver", "--person"])
    add = [server_binary, "key", "add", "ci", "--tenant", "acme", "--role", "approver"]
    service = subprocess.run(add, check=True, capture_output=True, text=True).stdout.strip()
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    serve = [server_binary, "serve", "--addr", f"127.0.0.1:{port}", "--record-dir", str(tmp_path / "records")]
    proc = subprocess.Popen(serve, stderr=subprocess.DEVNULL)
    for _ in range(100):
        try:
            socket.create_connection(("127.0.0.1", port), timeout=0.1).close()
            break
        except OSError:
            time.sleep(0.05)
    yield f"http://127.0.0.1:{port}/mcp", key, service
    proc.terminate()
    proc.wait()
