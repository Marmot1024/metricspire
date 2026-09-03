"""Verify, extract, and exec the architecture-specific MetricSpire service."""

from __future__ import annotations

import gzip
import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
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
    port = os.environ.get("DATABRICKS_APP_PORT", "")
    if not port.isascii() or not port.isdigit() or not 1 <= int(port) <= 65535:
        raise RuntimeError("DATABRICKS_APP_PORT must be an integer between 1 and 65535")

    root = Path(__file__).resolve().parent
    archive = root / f"metricspire-linux-{architecture}.gz"
    digest_path = archive.with_suffix(archive.suffix + ".sha256")
    expected = digest_path.read_text(encoding="utf-8").split()[0].lower()
    actual = hashlib.sha256(archive.read_bytes()).hexdigest()
    if actual != expected:
        raise RuntimeError(f"service archive checksum mismatch: {archive.name}")

    descriptor, executable_name = tempfile.mkstemp(prefix="metricspire-", dir=os.getenv("TMPDIR", "/tmp"))
    try:
        with os.fdopen(descriptor, "wb") as executable, gzip.open(archive, "rb") as compressed:
            shutil.copyfileobj(compressed, executable)
        os.chmod(executable_name, 0o700)

        migration_mode = os.environ.get("METRICSPIRE_MIGRATE_ONCE", "")
        if migration_mode:
            if migration_mode != "approved-staging":
                raise RuntimeError("METRICSPIRE_MIGRATE_ONCE contains an unsupported value")
            subprocess.run([executable_name, "migrate"], check=True)

        os.execv(
            executable_name,
            [
                executable_name,
                "serve",
                "--config",
                str(root / "config" / "runtime.json"),
                "--http-address",
                f"0.0.0.0:{port}",
            ],
        )
    finally:
        try:
            os.unlink(executable_name)
        except FileNotFoundError:
            pass


if __name__ == "__main__":
    main()
