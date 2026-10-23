"""Run one OpenCode V2 MCP acceptance turn with existing server authentication.

This is a maintainer test entry point, not an installer or an answer grader.
No custom agent, system prompt, skill or login is injected.
"""

import argparse
import contextlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time


def save_receipt(stream, receipt):
    if stream is not None:
        stream.seek(0)
        json.dump(receipt, stream, ensure_ascii=False, indent=2)
        stream.truncate()
        stream.flush()


def run(args):
    # Reserve evidence before any external call. Never run a query whose chosen
    # receipt path is already occupied or unavailable.
    stream = (os.fdopen(os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w")
              if args.output else contextlib.nullcontext(None))
    with stream as output:
        receipt = dict(prompt=args.prompt, model=args.model, session=args.session,
                       outcome="not_started")
        save_receipt(output, receipt)
        return run_turn(args, output, receipt)


def run_turn(args, output, receipt):
    directory = str(Path(args.directory).resolve(strict=True))
    env = {**os.environ, "PWD": directory}
    api = [args.opencode, "api", "--server", args.server, "--header",
           "x-opencode-directory:" + directory, "mcp.list"]
    started = time.monotonic()
    deadline = started + args.ready_timeout
    while True:
        try:
            check = subprocess.run(api, cwd=directory, env=env, capture_output=True,
                                   text=True, timeout=min(10, max(0.1, deadline - time.monotonic())))
            data = json.loads(check.stdout) if check.returncode == 0 else {}
            servers = data.get("data", []) if isinstance(data, dict) else []
            ready = isinstance(servers, list) and any(
                isinstance(s, dict) and s.get("name") == args.mcp_name
                and isinstance(s.get("status"), dict) and s["status"].get("status") == "connected"
                for s in servers)
        except (subprocess.TimeoutExpired, ValueError):
            ready = False
        if ready:
            break
        if time.monotonic() >= deadline:
            receipt["outcome"] = "readiness_failed"
            save_receipt(output, receipt)
            raise RuntimeError("MCP readiness failed before model invocation; check the existing connection. No login or model retry was attempted.")
        time.sleep(0.5)
    ready_seconds = round(time.monotonic() - started, 3)
    command = [args.opencode, "run", "--server", args.server, "--model", args.model,
               "--format", "json"]
    if args.session:
        command += ["--session", args.session]
    command += [args.prompt]
    started = time.monotonic()
    receipt["outcome"] = "model_invocation_started"
    save_receipt(output, receipt)
    timed_out = False
    try:
        result = subprocess.run(command, cwd=directory, env=env, capture_output=True,
                                text=True, timeout=args.timeout)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        partial = error.stdout or ""
        if isinstance(partial, bytes):
            partial = partial.decode(errors="replace")
        result = subprocess.CompletedProcess(command, 124, partial, "")
    events = []
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
            if isinstance(event, dict):
                events.append(event)
        except ValueError:
            continue
    tools = [e.get("part", {}) for e in events if e.get("type") == "tool_use"]
    completed = [p for p in tools if p.get("tool", "").startswith(args.mcp_name + "_")
                 and p.get("state", {}).get("status") == "completed"]
    errors = [e for e in events if e.get("type") == "error"]
    receipt = dict(prompt=args.prompt, model=args.model, session=next((e.get("sessionID") for e in events if e.get("sessionID")), args.session),
                   readiness_seconds=ready_seconds, turn_seconds=round(time.monotonic() - started, 3),
                   cli_exit=result.returncode, timed_out=timed_out, completed_mcp_calls=len(completed),
                   custom_context_injected=False, semantic_acceptance="requires trace and answer review",
                   stderr_bytes=len(result.stderr.encode()), events=events)
    save_receipt(output, receipt)
    for event in events:
        if event.get("type") == "text":
            print(event.get("part", {}).get("text", ""))
    print(json.dumps({k: v for k, v in receipt.items() if k not in ("events", "prompt")}, ensure_ascii=False), file=sys.stderr)
    if timed_out:
        raise RuntimeError("Model turn timed out; partial events were retained when an output path was supplied. Inspect the existing session/jobs; do not blindly repeat the question.")
    if result.returncode or errors or not completed:
        raise RuntimeError("Turn did not establish a completed MCP interaction. CLI exit zero or tool-shaped answer text alone is insufficient; review the receipt. No automatic model retry was attempted.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("prompt", help="The unchanged user question; use only approved neutral fixtures for external-model acceptance")
    parser.add_argument("--directory", required=True, help="Existing isolated OpenCode project")
    parser.add_argument("--server", required=True, help="Existing OpenCode server URL (not the MetricSpire MCP URL)")
    parser.add_argument("--model", required=True)
    parser.add_argument("--opencode", default="opencode")
    parser.add_argument("--mcp-name", default="metricspire")
    parser.add_argument("--session", help="Continue the same session for a user's clarification")
    parser.add_argument("--output", help="New private receipt file; stores model/tool events, never server passwords")
    parser.add_argument("--ready-timeout", type=float, default=30)
    parser.add_argument("--timeout", type=float, default=240)
    args = parser.parse_args()
    if args.ready_timeout <= 0 or args.timeout <= 0:
        parser.error("timeouts must be positive")
    try:
        run(args)
    except (OSError, RuntimeError) as error:
        # External CLI stderr can contain credentials; never echo it here.
        print(str(error) if isinstance(error, RuntimeError) else "Local executable, directory or receipt file is unavailable; inspect the path and permissions.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
