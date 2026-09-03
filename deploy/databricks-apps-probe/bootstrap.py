"""Verify, extract, and exec the architecture-specific Go runtime probe."""

from __future__ import annotations

import gzip
import hashlib
import os
from pathlib import Path
import platform
import shutil
import tempfile


ARCHITECTURES = {
    "aarch64": "arm64",
    "arm64": "arm64",
    "x86_64": "amd64",
}


def main() -> None:
    architecture = ARCHITECTURES.get(platform.machine().lower())
    if architecture is None:
        raise RuntimeError(f"unsupported runtime architecture: {platform.machine()}")

    root = Path(__file__).resolve().parent
    archive = root / f"metricspire-app-probe-linux-{architecture}.gz"
    digest_path = archive.with_suffix(archive.suffix + ".sha256")
    expected = digest_path.read_text(encoding="utf-8").split()[0].lower()
    actual = hashlib.sha256(archive.read_bytes()).hexdigest()
    if actual != expected:
        raise RuntimeError(f"probe archive checksum mismatch: {archive.name}")

    descriptor, executable_name = tempfile.mkstemp(prefix="metricspire-app-probe-", dir=os.getenv("TMPDIR", "/tmp"))
    try:
        with os.fdopen(descriptor, "wb") as executable, gzip.open(archive, "rb") as compressed:
            shutil.copyfileobj(compressed, executable)
        os.chmod(executable_name, 0o700)
        os.execv(executable_name, [executable_name])
    finally:
        try:
            os.unlink(executable_name)
        except FileNotFoundError:
            pass


if __name__ == "__main__":
    main()
