#!/usr/bin/env python3
"""Optional local Codex HTTP-header helper for a Databricks Apps MCP URL.

Run `databricks auth login` yourself first. The CLI owns its OAuth cache; this
helper only asks it for a fresh user access token and returns one JSON header
map to Codex. Never invoke this command in a terminal to inspect its output.
"""

import argparse
import json
import os
import subprocess
import sys


def headers(profile: str) -> dict[str, str]:
    if not profile or profile.startswith("-") or len(profile) > 128:
        raise ValueError("invalid Databricks CLI profile")

    environment = os.environ.copy()
    # A caller's unrelated static token must not shadow this named U2M login.
    environment.pop("DATABRICKS_TOKEN", None)
    result = subprocess.run(
        ["databricks", "auth", "token", "--profile", profile],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        check=True,
        timeout=20,
        text=True,
        env=environment,
    )
    token = json.loads(result.stdout).get("access_token")
    if not isinstance(token, str) or not token or "\n" in token or "\r" in token:
        raise ValueError("Databricks CLI did not return a usable access token")
    return {"Authorization": "Bearer " + token}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True, help="Databricks CLI U2M profile for the App workspace")
    args = parser.parse_args()
    try:
        result = headers(args.profile)
    except (OSError, subprocess.SubprocessError, ValueError):
        print("Databricks CLI login unavailable; run databricks auth login for the App workspace", file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
