"""Finds the nodloop binary the client runs and fetches the release one when there is none."""

from __future__ import annotations

import hashlib
import io
import os
import platform
import shutil
import tarfile
import urllib.request
from pathlib import Path

REPO = "jeon-jihyeon/nodloop"


def home_bin() -> Path:
    return Path.home() / ".nodloop" / "bin"


def find(version: str) -> str:
    """The binary to run.

    1. NODLOOP_BIN when it is set
    2. the stable link the Claude Code plugin keeps under ~/.nodloop/bin
    3. nodloop on PATH
    4. the release of the version, fetched once into ~/.nodloop/bin/v<version> and checked against its checksums
    """
    if env := os.environ.get("NODLOOP_BIN"):
        return env
    stable = home_bin() / "nodloop"
    if stable.exists():
        return str(stable)
    if found := shutil.which("nodloop"):
        return found
    return str(fetch(version))


def archive_name() -> str:
    system = platform.system().lower()
    machine = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine().lower()]
    return f"nodloop_{system}_{machine}.tar.gz"


def fetch(version: str) -> Path:
    """The release binary of the version, refused when its archive does not match checksums.txt."""
    target = home_bin() / f"v{version}" / "nodloop"
    if target.exists():
        return target
    base = f"https://github.com/{REPO}/releases/download/v{version}"
    name = archive_name()
    archive = urllib.request.urlopen(f"{base}/{name}", timeout=60).read()
    sums = urllib.request.urlopen(f"{base}/checksums.txt", timeout=30).read().decode()
    want = next((line.split()[0] for line in sums.splitlines() if line.split()[1:] == [name]), "")
    if not want or hashlib.sha256(archive).hexdigest() != want:
        raise RuntimeError(f"checksum of {base}/{name} does not match checksums.txt")
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        member = tar.extractfile("nodloop")
        if member is None:
            raise RuntimeError(f"{name} holds no nodloop binary")
        data = member.read()
    target.parent.mkdir(parents=True, exist_ok=True)
    partial = target.with_suffix(".partial")
    partial.write_bytes(data)
    partial.chmod(0o755)
    partial.replace(target)
    return target
