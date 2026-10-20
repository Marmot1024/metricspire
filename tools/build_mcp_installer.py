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
    if set(c) != {"url", "workspace_host"}:
        raise ValueError("Only public MCP URL and workspace_host are accepted")
    for key in ("url", "workspace_host"):
        raw = c[key]
        if not isinstance(raw, str) or not raw or any(ord(ch) < 32 for ch in raw):
            raise ValueError("Invalid public connection field")
        u = urlsplit(raw)
        _ = u.port
        if (u.scheme != "https" or not u.hostname or u.username or u.password
                or u.query or u.fragment):
            raise ValueError("Use HTTPS addresses without credentials, query or fragment")
        if key == "workspace_host" and (u.path or u.port):
            raise ValueError("workspace_host must be an HTTPS origin")
        if key == "url" and u.path != "/api/v1/mcp":
            raise ValueError("MCP URL must end in /api/v1/mcp")


def validate_oidc_connection(c):
    if set(c) != {"url", "issuer", "client_id", "scopes"}:
        raise ValueError("OIDC bundle accepts public URL, issuer, public client_id and scopes only")
    for key in ("url", "issuer"):
        raw = c[key]
        if not isinstance(raw, str) or any(ord(ch) < 32 for ch in raw):
            raise ValueError("Invalid OIDC connection URL")
        u = urlsplit(raw)
        _ = u.port
        if u.scheme != "https" or not u.hostname or u.username or u.password or u.query or u.fragment:
            raise ValueError("OIDC connection requires credential-free HTTPS URLs")
        if key == "url" and u.path not in ("/mcp", "/api/v1/mcp"):
            raise ValueError("Invalid MCP endpoint")
    if not isinstance(c["client_id"], str) or not c["client_id"].strip() or len(c["client_id"]) > 512:
        raise ValueError("Registered public OAuth client_id is required")
    scopes = c["scopes"]
    if (not isinstance(scopes, list) or not 1 <= len(scopes) <= 16
            or any(not isinstance(x, str) or not x or len(x) > 128 or any(ch in x for ch in " ,\t\r\n\x00") for x in scopes)
            or len(set(scopes)) != len(scopes)):
        raise ValueError("Configure unique provider-approved scopes")


def build(c, platforms, output, oidc=False):
    (validate_oidc_connection if oidc else validate_connection)(c)
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
                                    "-o", str(binary), "./tools/mcp-oidc" if oidc else "./tools/mcp-installer"],
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
    provider = parser.add_mutually_exclusive_group(required=True)
    provider.add_argument("--workspace-host", help="Databricks workspace HTTPS origin")
    provider.add_argument("--oidc-issuer", help="Reviewed standard OIDC issuer")
    parser.add_argument("--oauth-client-id", help="Registered public MCP client, without secret")
    parser.add_argument("--scope", action="append", help="Provider-approved OAuth scope (repeatable, OIDC only)")
    parser.add_argument("--platform", action="append", choices=PLATFORMS,
                        help="Default: all four platforms; restrict only for local testing")
    parser.add_argument("--output", type=Path, default=ROOT / "dist/metricspire-install.sh")
    args = parser.parse_args()
    if args.oidc_issuer:
        c = dict(url=args.url, issuer=args.oidc_issuer, client_id=args.oauth_client_id, scopes=args.scope)
    else:
        if args.oauth_client_id or args.scope:
            parser.error("OAuth client/scopes require --oidc-issuer")
        c = dict(url=args.url, workspace_host=args.workspace_host)
    try:
        digest = build(c, args.platform or PLATFORMS, args.output, oidc=bool(args.oidc_issuer))
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, "Build failed: " + str(error) + "\n")
    print(str(args.output) + "\nSHA-256: " + digest)


if __name__ == "__main__":
    main()
