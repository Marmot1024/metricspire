"""Render non-secret staging configuration into the ignored upload directory."""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import sys
from urllib.parse import urlparse


ENDPOINT = re.compile(r"^projects/[a-z0-9-]+/branches/[a-z0-9-]+/endpoints/[a-z0-9-]+$")
APP_NAME = re.compile(r"^[a-z][a-z0-9-]{1,29}$")
NAMESPACE = re.compile(r"^[a-z][a-z0-9_-]{0,127}$")


def required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def add_online_config(runtime: dict, config_directory: Path, source: Path) -> None:
    """Bundle only explicit neutral config artifacts, never a credential URL."""
    online = json.loads(source.read_text(encoding="utf-8"))
    allowed = {"tenant", "namespaces", "data_access", "resources", "max_data_age", "timeout", "max_concurrency", "bindings", "policies"}
    if not isinstance(online, dict) or set(online) - allowed:
        raise RuntimeError("online package must contain only an OnlineConfig object")
    if online.get("tenant") != runtime["authentication"]["databricks_apps"]["tenant"] or online.get("data_access") != "tenant_shared":
        raise RuntimeError("online package tenant and tenant_shared access must be explicitly reviewed")
    if not online.get("namespaces") or not online.get("policies") or not online.get("bindings") or not online.get("resources") or not online.get("max_data_age"):
        raise RuntimeError("online package requires independent policies, bindings, sources and freshness")
    artifacts: dict[str, str] = {}
    for kind in ("policies", "bindings"):
        for route in online[kind]:
            name = route.get("path", "")
            # Flat, task-specific JSON files prevent traversal, collisions with
            # analytical config and accidental directory-wide asset uploads.
            if not re.fullmatch(r"online-[a-z0-9-]+\.json", name):
                raise RuntimeError("online artifact paths must be flat online-*.json names")
            artifact = source.parent / name
            if artifact.is_symlink() or not artifact.is_file():
                raise RuntimeError("online artifacts must be regular files")
            content = artifact.read_text(encoding="utf-8")
            payload = json.loads(content)
            if kind == "policies":
                if payload.get("kind") != "PolicySource" or payload.get("tenant") != online["tenant"]:
                    raise RuntimeError("online reader policy contract or tenant is invalid")
                for rule in payload.get("rules", []):
                    if rule.get("effect") == "allow" and (
                        rule.get("roles") or any(
                            not explicit_values(rule.get(key))
                            for key in ("principals", "metrics", "dimensions")
                        )
                    ):
                        raise RuntimeError("online policies require explicit readers and data scopes")
            elif payload.get("kind") != "SourceBinding" or payload.get("engine") != "postgres_online":
                raise RuntimeError("online bindings must target postgres_online")
            if name in artifacts and artifacts[name] != content:
                raise RuntimeError("online artifact collision")
            artifacts[name] = content
    runtime["online"] = online
    for name, content in artifacts.items():
        (config_directory / name).write_text(content, encoding="utf-8")


def explicit_values(values) -> bool:
    return isinstance(values, list) and bool(values) and all(
        isinstance(value, str) and value.strip() and value == value.strip() and value != "*"
        for value in values
    )


def main() -> None:
    if len(sys.argv) != 2:
        raise RuntimeError("usage: render.py OUTPUT_DIRECTORY")
    output = Path(sys.argv[1]).resolve()
    config_directory = output / "config"
    config_directory.mkdir(parents=True, exist_ok=True)

    app_name = required("METRICSPIRE_APPS_NAME")
    workspace_id = required("METRICSPIRE_APPS_WORKSPACE_ID")
    public_url = required("METRICSPIRE_APPS_PUBLIC_URL")
    publisher_group_id = required("METRICSPIRE_APPS_PUBLISHER_GROUP_ID")
    endpoint = required("METRICSPIRE_APPS_LAKEBASE_ENDPOINT")
    trial_namespaces = [
        value.strip()
        for value in os.environ.get("METRICSPIRE_APPS_TRIAL_NAMESPACES", "").split(",")
        if value.strip()
    ]

    parsed_url = urlparse(public_url)
    if not APP_NAME.fullmatch(app_name):
        raise RuntimeError("METRICSPIRE_APPS_NAME must be a valid Databricks App name")
    if not workspace_id.isascii() or not workspace_id.isdigit():
        raise RuntimeError("METRICSPIRE_APPS_WORKSPACE_ID must contain only digits")
    if parsed_url.scheme != "https" or not parsed_url.netloc or parsed_url.username is not None or parsed_url.path not in ("", "/") or parsed_url.query or parsed_url.fragment:
        raise RuntimeError("METRICSPIRE_APPS_PUBLIC_URL must be an HTTPS origin")
    if any(character.isspace() for character in publisher_group_id) or len(publisher_group_id) > 256:
        raise RuntimeError("METRICSPIRE_APPS_PUBLISHER_GROUP_ID is invalid")
    if not ENDPOINT.fullmatch(endpoint):
        raise RuntimeError("METRICSPIRE_APPS_LAKEBASE_ENDPOINT must be a Lakebase endpoint resource name")
    if len(set(trial_namespaces)) != len(trial_namespaces) or any(not NAMESPACE.fullmatch(value) for value in trial_namespaces):
        raise RuntimeError("METRICSPIRE_APPS_TRIAL_NAMESPACES must be a comma-separated list of unique namespaces")

    runtime = {
        "api_version": "metricspire.io/v1alpha1",
        "kind": "RuntimeConfig",
        "http": {
            "address": "0.0.0.0:8000",
            "public_url": public_url.rstrip("/"),
            "max_body_bytes": 1048576,
            "control_timeout": "15s",
            "query_timeout": "2m",
        },
        "authentication": {
            "provider": "databricks_apps",
            "databricks_apps": {
                "expected_app_name": app_name,
                "expected_workspace_id": workspace_id,
                "tenant": "acceptance",
                "query_role": "analyst",
                "publisher_group_id": publisher_group_id,
            },
        },
        "policies": [
            {
                "namespace": "acceptance",
                "model_name": "tpch_orders",
                "tenant": "acceptance",
                "path": "policy.yaml",
            }
        ],
        "bindings": [
            {
                "namespace": "acceptance",
                "model_name": "tpch_orders",
                "path": "binding.json",
            }
        ],
    }
    if trial_namespaces:
        runtime["release_policy"] = {"trial_namespaces": trial_namespaces}
    online_config = os.environ.get("METRICSPIRE_APPS_ONLINE_CONFIG", "").strip()
    if online_config:
        add_online_config(runtime, config_directory, Path(online_config).resolve())
    (config_directory / "runtime.json").write_text(
        json.dumps(runtime, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    app_yaml = (
        "command:\n"
        "  - python\n"
        "  - bootstrap.py\n"
        "env:\n"
        "  - name: DATABRICKS_SQL_WAREHOUSE_ID\n"
        "    valueFrom: metricspire-warehouse\n"
        "  - name: METRICSPIRE_LAKEBASE_ENDPOINT\n"
        f"    value: {endpoint}\n"
        "  - name: METRICSPIRE_DATABASE_SCHEMA\n"
        "    value: metricspire\n"
    )
    migration_mode = os.environ.get("METRICSPIRE_APPS_MIGRATE_ONCE", "").strip()
    if migration_mode:
        if migration_mode != "approved-staging":
            raise RuntimeError("METRICSPIRE_APPS_MIGRATE_ONCE contains an unsupported value")
        app_yaml += (
            "  - name: METRICSPIRE_MIGRATE_ONCE\n"
            "    value: approved-staging\n"
        )
    if trial_namespaces:
        app_yaml += (
            "  - name: METRICSPIRE_TRIAL_RELEASES\n"
            "    value: approved-trial\n"
        )
    if online_config:
        app_yaml += (
            "  - name: METRICSPIRE_ONLINE_DATABASE_URL\n"
            "    valueFrom: metricspire-online-database-url\n"
        )
    (output / "app.yaml").write_text(app_yaml, encoding="utf-8")


if __name__ == "__main__":
    main()
