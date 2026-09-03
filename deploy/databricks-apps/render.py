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


def required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


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

    parsed_url = urlparse(public_url)
    if not APP_NAME.fullmatch(app_name):
        raise RuntimeError("METRICSPIRE_APPS_NAME must be a valid Databricks App name")
    if not workspace_id.isascii() or not workspace_id.isdigit():
        raise RuntimeError("METRICSPIRE_APPS_WORKSPACE_ID must contain only digits")
    if parsed_url.scheme != "https" or not parsed_url.netloc or parsed_url.path not in ("", "/") or parsed_url.query or parsed_url.fragment:
        raise RuntimeError("METRICSPIRE_APPS_PUBLIC_URL must be an HTTPS origin")
    if any(character.isspace() for character in publisher_group_id) or len(publisher_group_id) > 256:
        raise RuntimeError("METRICSPIRE_APPS_PUBLISHER_GROUP_ID is invalid")
    if not ENDPOINT.fullmatch(endpoint):
        raise RuntimeError("METRICSPIRE_APPS_LAKEBASE_ENDPOINT must be a Lakebase endpoint resource name")

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
    (output / "app.yaml").write_text(app_yaml, encoding="utf-8")


if __name__ == "__main__":
    main()
