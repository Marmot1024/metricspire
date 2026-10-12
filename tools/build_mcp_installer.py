#!/usr/bin/env python3
"""Build one self-extracting installer. Python and Go are build-time only."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]
PLATFORMS = ("darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64")
SHELL = """#!/bin/sh
set -eu
umask 077
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) platform=darwin-arm64 ;;
  Darwin-x86_64) platform=darwin-amd64 ;;
  Linux-aarch64|Linux-arm64) platform=linux-arm64 ;;
  Linux-x86_64) platform=linux-amd64 ;;
  *) echo 'MetricSpire: 支持 macOS / Linux 的 arm64 和 amd64。' >&2; exit 1 ;;
esac
work=$(mktemp -d "${TMPDIR:-/tmp}/metricspire-install.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
line=$(awk '/^__METRICSPIRE_PAYLOAD__$/{print NR+1; exit}' "$0")
test -n "$line" || { echo 'MetricSpire: 安装包不完整。' >&2; exit 1; }
tail -n +"$line" "$0" > "$work/payload.tgz"
if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$work/payload.tgz" | awk '{print $1}')
elif command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/payload.tgz" | awk '{print $1}')
else
  echo 'MetricSpire: 缺少 SHA-256 校验工具，已停止。' >&2; exit 1
fi
test "$actual" = '@DIGEST@' || { echo 'MetricSpire: 安装包校验失败，请重新获取文件。' >&2; exit 1; }
tar -xzf "$work/payload.tgz" -C "$work" "$platform/metricspire-install" connection.json
chmod 700 "$work/$platform/metricspire-install"
"$work/$platform/metricspire-install" --bundle "$work/connection.json" "$@"
exit $?
__METRICSPIRE_PAYLOAD__
"""


def validate_connection(c):
    if set(c) != {"url", "client_id", "callback_url", "callback_port"}:
        raise ValueError("Only public URL, client_id, callback_url and callback_port are accepted")
    for key in ("url", "client_id", "callback_url"):
        if not isinstance(c[key], str) or not c[key] or any(ord(ch) < 32 for ch in c[key]):
            raise ValueError("Invalid public connection field")
    u = urlsplit(c["url"])
    # urlsplit defers port syntax/range validation until this property is read.
    _ = u.port
    if u.scheme != "https" or not u.hostname or u.username or u.password or u.query or u.fragment:
        raise ValueError("MCP URL must use HTTPS without credentials, query or fragment")
    u = urlsplit(c["callback_url"])
    port = c["callback_port"]
    if (u.scheme != "http" or u.hostname not in ("localhost", "127.0.0.1", "::1")
            or u.username or u.password or not u.path or u.query or u.fragment
            or type(port) is not int or not 1 <= port <= 65535
            or u.port is not None and u.port != port):
        raise ValueError("Use the registered loopback callback URL and matching port")


def build(c, platforms, output):
    validate_connection(c)
    # Environment-specific artifacts must remain in ignored dist/, never docs/.
    output = output.resolve()
    dist = (ROOT / "dist").resolve()
    if dist not in output.parents:
        raise ValueError("Installer output must be inside this checkout's ignored dist/ directory")
    if not platforms or any(p not in PLATFORMS for p in platforms):
        raise ValueError("Unsupported build platform")
    with tempfile.TemporaryDirectory(prefix="metricspire-installer-build-") as tmp:
        payload = io.BytesIO()
        with gzip.GzipFile(fileobj=payload, mode="wb", mtime=0) as zipped:
            with tarfile.open(fileobj=zipped, mode="w") as archive:
                def add(name, data, mode=0o600):
                    item = tarfile.TarInfo(name)
                    item.size, item.mode, item.mtime = len(data), mode, 0
                    archive.addfile(item, io.BytesIO(data))
                add("connection.json", json.dumps(c, ensure_ascii=True).encode())
                add("THIRD_PARTY_LICENSES", (ROOT / "tools/mcp-installer/THIRD_PARTY_LICENSES").read_bytes())
                for platform in platforms:
                    goos, goarch = platform.split("-")
                    binary = Path(tmp) / platform
                    env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
                    subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w",
                                    "-o", str(binary), "./tools/mcp-installer"],
                                   cwd=ROOT, env=env, check=True)
                    add(platform + "/metricspire-install", binary.read_bytes(), 0o700)
        data = payload.getvalue()
        content = SHELL.replace("@DIGEST@", hashlib.sha256(data).hexdigest()).encode() + data
        output.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(dir=output.parent, delete=False) as staged:
            staged.write(content)
            temporary = Path(staged.name)
        try:
            temporary.chmod(0o700)
            temporary.replace(output)
        finally:
            temporary.unlink(missing_ok=True)
    return hashlib.sha256(content).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--client-id", required=True, help="Public client ID; never a client secret")
    parser.add_argument("--callback-url", required=True, help="Exact Codex callback base registered with provider")
    parser.add_argument("--callback-port", type=int, required=True)
    parser.add_argument("--platform", action="append", choices=PLATFORMS,
                        help="Default: all four platforms; restrict only for local testing")
    parser.add_argument("--output", type=Path, default=ROOT / "dist/metricspire-install.sh")
    args = parser.parse_args()
    c = dict(url=args.url, client_id=args.client_id, callback_url=args.callback_url,
             callback_port=args.callback_port)
    try:
        digest = build(c, args.platform or PLATFORMS, args.output)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, "Build failed: " + str(error) + "\n")
    print(str(args.output) + "\nSHA-256: " + digest)


if __name__ == "__main__":
    main()
