#!/usr/bin/env python3
"""Deploy an immutable reviewed commit, preserving the App's frozen config.

Management uses an existing CLI profile; every data query uses native Codex MCP.
No credentials, user grants or App scopes are created/changed by this command.
"""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]


def extract_source(archive, destination):
    # Python 3.9 compatibility without unsafe tar traversal or link extraction.
    with tarfile.open(archive) as tar:
        for member in tar.getmembers():
            path = Path(member.name)
            if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
                raise RuntimeError("source archive contains an unsafe path or link")
        tar.extractall(destination)


def run(args, *, cwd=None, env=None, stream=False, timeout=300):
    result = subprocess.run(args, cwd=cwd, env=env, stdout=None if stream else subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        # Raw CLI / server errors can contain credentials. Gate output is safe.
        if stream and result.stderr:
            print(result.stderr.decode(errors="replace"), end="", flush=True)
        raise RuntimeError(f"{Path(args[0]).name} operation failed")
    return result.stdout


def build(source, output, package, commit):
    env = dict(os.environ, GOWORK="off", CGO_ENABLED="0")
    run(["go", "build", "-trimpath", "-o", str(output / "release-gate"), "./tools/release-gate"], cwd=source, env=env)
    run(["go", "build", "-trimpath", "-o", str(output / "metricspire-check"), "./cmd/metricspire"], cwd=source, env=env)
    for arch in ("amd64", "arm64"):
        binary = output / f"service-{arch}"
        run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version=0.1.0-dev+{commit}",
             "-o", str(binary), "./cmd/metricspire"], cwd=source, env=dict(env, GOOS="linux", GOARCH=arch))
        archive = package / f"metricspire-linux-{arch}.gz"
        archive.write_bytes(gzip.compress(binary.read_bytes(), mtime=0))
        archive.with_suffix(".gz.sha256").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
    shutil.copyfile(source / "deploy/databricks-apps/bootstrap.py", package / "bootstrap.py")


def preserve_config(frozen, package):
    # Do not re-render defaults: that would drop live business/online policies.
    if not (frozen / "app.yaml").is_file() or not (frozen / "config/runtime.json").is_file():
        raise RuntimeError("frozen deployment is not a supported MetricSpire package")
    shutil.copyfile(frozen / "app.yaml", package / "app.yaml")
    for path in (frozen / "config").rglob("*"):
        if path.is_symlink() or (path.is_file() and path.suffix not in (".json", ".yaml", ".yml")):
            raise RuntimeError("unexpected file in frozen config")
    shutil.copytree(frozen / "config", package / "config")


def configure_exploration(package, namespace, catalog_path, warehouse, has_acceptance):
    runtime_path = package / "config/runtime.json"
    runtime = json.loads(runtime_path.read_text())
    existing = runtime.get("exploration")
    if (existing or catalog_path) and not has_acceptance:
        raise RuntimeError("configured exploration requires analysis query/result acceptance fixtures")
    if not catalog_path:
        return  # Existing catalogs and execution budgets are preserved verbatim.
    catalog = json.loads(catalog_path.read_text())
    if catalog.get("tenant") != runtime["authentication"]["databricks_apps"]["tenant"] or catalog.get("namespace") != namespace:
        raise RuntimeError("approved exploration catalog does not match target tenant/namespace")
    target = package / "config/analysis-acceptance.json"
    if existing:
        if (existing.get("warehouse_id") != warehouse
                or "analysis-acceptance.json" not in existing.get("catalog_paths", [])
                or not target.is_file() or json.loads(target.read_text()) != catalog):
            raise RuntimeError("exploration differs from the approved existing catalog; review before replacing")
        return
    shutil.copyfile(catalog_path, target)
    runtime["exploration"] = dict(catalog_paths=["analysis-acceptance.json"], warehouse_id=warehouse,
                                  concurrency=2, capacity=20, result_ttl="30m")
    runtime_path.write_text(json.dumps(runtime, indent=2) + "\n")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for key in ("app", "profile", "auth-profile", "commit", "mcp-url", "issuer", "workspace-parent"):
        p.add_argument("--" + key, required=True)
    p.add_argument("--namespace", default="acceptance")
    p.add_argument("--query", type=Path, default=ROOT / "testdata/acceptance/databricks-tpch/query.json")
    p.add_argument("--expected", type=Path, default=ROOT / "testdata/acceptance/databricks-tpch/expected.json")
    p.add_argument("--exploration-catalog", type=Path)
    p.add_argument("--exploration-warehouse")
    p.add_argument("--analysis-query", type=Path)
    p.add_argument("--analysis-expected", type=Path)
    a = p.parse_args()
    if bool(a.exploration_catalog) != bool(a.exploration_warehouse) or bool(a.analysis_query) != bool(a.analysis_expected):
        p.error("catalog/warehouse and analysis query/expected must be supplied in pairs")
    if a.exploration_catalog and not a.analysis_query:
        p.error("a new exploration catalog requires query/result acceptance fixtures")
    commit = run(["git", "rev-parse", "--verify", a.commit + "^{commit}"], cwd=ROOT).decode().strip()
    db = lambda *args: ["databricks", *args, "--profile", a.profile]
    get_app = lambda: json.loads(run(db("apps", "get", a.app, "--output", "json")))
    before = get_app()
    previous = before["active_deployment"]
    if previous["status"]["state"] != "SUCCEEDED":
        raise RuntimeError("current deployment is not successful; stop before mutation")
    frozen_path = previous["deployment_artifacts"]["source_code_path"]
    print("TARGET", commit, "ROLLBACK", previous["deployment_id"], flush=True)
    with tempfile.TemporaryDirectory(prefix="metricspire-release-") as temp:
        os.chmod(temp, 0o700)
        work = Path(temp)
        source, frozen, package = (work / name for name in ("source", "frozen", "package"))
        source.mkdir(); package.mkdir()
        archive = work / "source.tar"
        archive.write_bytes(run(["git", "archive", commit], cwd=ROOT))
        extract_source(archive, source)
        run(db("workspace", "export-dir", frozen_path, str(frozen)))
        preserve_config(frozen, package)
        configure_exploration(package, a.namespace, a.exploration_catalog, a.exploration_warehouse, bool(a.analysis_query))
        build(source, work, package, commit)
        gate = [str(work / "release-gate"), "--app", a.app, "--profile", a.profile,
                "--auth-profile", a.auth_profile, "--commit", commit, "--mcp-url", a.mcp_url, "--issuer", a.issuer,
                "--namespace", a.namespace]
        fixture = ["--query", str(a.query.resolve()), "--expected", str(a.expected.resolve())]
        run(gate + fixture + ["--preflight"], stream=True)
        if a.exploration_catalog:
            run([str(work / "metricspire-check"), "validate-analysis", "--catalog", str(a.exploration_catalog.resolve())])
        # Detect concurrent deployments before making any remote change.
        if get_app()["active_deployment"]["deployment_id"] != previous["deployment_id"]:
            raise RuntimeError("App changed during preflight; stop without deploying")
        destination = a.workspace_parent.rstrip("/") + "/metricspire-" + commit[:12] + "-" + uuid.uuid4().hex[:8]
        run(db("workspace", "import-dir", str(package), destination))
        deployed_id = None
        try:
            deployed = json.loads(run(db("apps", "deploy", a.app, "--source-code-path", destination,
                                         "--mode", "SNAPSHOT", "--output", "json"), timeout=600))
            deployed_id = deployed["deployment_id"]
            run(gate + fixture, stream=True)
            if a.analysis_query:
                run(gate + ["--query", str(a.analysis_query.resolve()), "--expected", str(a.analysis_expected.resolve())], stream=True)
            if get_app()["active_deployment"]["deployment_id"] != deployed_id:
                raise RuntimeError("App changed during acceptance")
        except Exception:
            current = get_app()["active_deployment"]
            if current.get("source_code_path") == destination and (deployed_id is None or current["deployment_id"] == deployed_id):
                print("ACCEPTANCE FAILED; restoring frozen previous deployment", flush=True)
                run(db("apps", "deploy", a.app, "--source-code-path", frozen_path, "--mode", "SNAPSHOT", "--output", "json"), timeout=600)
                run(gate + fixture + ["--preflight"], stream=True)
                print("ROLLBACK VERIFIED", flush=True)
            else:
                print("No rollback attempted: this release is not the active deployment", flush=True)
            raise
        print("DEPLOYMENT AND ACCEPTANCE PASSED", commit, deployed_id, flush=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        # Avoid dumping subprocess environments or raw App responses.
        print("RELEASE FAILED:", type(error).__name__, str(error) if isinstance(error, RuntimeError) else "check release inputs/state")
        raise SystemExit(1)
