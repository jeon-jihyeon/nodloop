import subprocess
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
