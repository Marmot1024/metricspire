"""Neutral package regression; never reads a credential or contacts Databricks."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("apps_render", Path(__file__).with_name("render.py"))
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class RenderTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.output = self.root / "output"
        self.output.mkdir()
        self.runtime = {"authentication": {"databricks_apps": {"tenant": "acceptance"}}}
        self.online = {
            "tenant": "acceptance", "namespaces": ["acceptance"],
            "data_access": "tenant_shared", "max_data_age": "5m",
            "resources": [{"kind": "table", "schema": "synthetic_serving", "table": "daily_sales"}],
            "policies": [{"namespace": "acceptance", "model_name": "daily_sales", "tenant": "acceptance", "path": "online-readers.json"}],
            "bindings": [{"namespace": "acceptance", "model_name": "daily_sales", "path": "online-binding.json"}],
        }
        self.reader = {"kind": "PolicySource", "tenant": "acceptance", "rules": [{"effect": "allow", "principals": ["synthetic-reader"], "metrics": ["revenue"], "dimensions": ["date"]}]}
        self.write("online-readers.json", self.reader)
        self.write("online-binding.json", {"kind": "SourceBinding", "engine": "postgres_online"})

    def write(self, name, payload):
        path = self.root / name
        path.write_text(json.dumps(payload), encoding="utf-8")
        return path

    def render_online(self, online=None):
        source = self.write("online.json", self.online if online is None else online)
        renderer.add_online_config(self.runtime, self.output, source)

    def test_explicit_package_copies_only_referenced_configs(self):
        self.write("unreferenced.json", {"should_not_copy": True})
        self.render_online()
        self.assertEqual(self.runtime["online"], self.online)
        self.assertEqual(sorted(p.name for p in self.output.iterdir()), ["online-binding.json", "online-readers.json"])

    def test_rejects_credentials_missing_policy_tenant_and_traversal(self):
        for change in (
            {"database_url": "postgres://synthetic-not-a-credential"},
            {"policies": []}, {"tenant": "other"},
            {"policies": [{"path": "../online-readers.json"}]},
        ):
            with self.subTest(change=change):
                online = copy.deepcopy(self.online)
                online.update(change)
                with self.assertRaises(RuntimeError):
                    self.render_online(online)

    def test_rejects_broad_reader_grants(self):
        for key, values in (("roles", ["analyst"]), ("principals", ["*"]), ("metrics", ["*"]), ("dimensions", [])):
            with self.subTest(key=key):
                reader = copy.deepcopy(self.reader)
                reader["rules"][0][key] = values
                self.write("online-readers.json", reader)
                with self.assertRaises(RuntimeError):
                    self.render_online()

    def test_default_package_does_not_enable_online_or_migration(self):
        env = {
            "METRICSPIRE_APPS_NAME": "metricspire-synthetic",
            "METRICSPIRE_APPS_WORKSPACE_ID": "314",
            "METRICSPIRE_APPS_PUBLIC_URL": "https://synthetic.example.com",
            "METRICSPIRE_APPS_PUBLISHER_GROUP_ID": "synthetic-publishers",
            "METRICSPIRE_APPS_LAKEBASE_ENDPOINT": "projects/synthetic/branches/staging/endpoints/primary",
        }
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", ["render.py", str(self.output)]):
            renderer.main()
        runtime = json.loads((self.output / "config" / "runtime.json").read_text())
        app_yaml = (self.output / "app.yaml").read_text()
        self.assertNotIn("online", runtime)
        self.assertNotIn("ONLINE_DATABASE_URL", app_yaml)
        self.assertNotIn("MIGRATE_ONCE", app_yaml)
        self.write("online.json", self.online)
        env["METRICSPIRE_APPS_ONLINE_CONFIG"] = str(self.root / "online.json")
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", ["render.py", str(self.output)]):
            renderer.main()
        self.assertIn("online", json.loads((self.output / "config" / "runtime.json").read_text()))
        self.assertIn("valueFrom: metricspire-online-database-url", (self.output / "app.yaml").read_text())

    def test_public_origin_cannot_embed_a_credential(self):
        env = {
            "METRICSPIRE_APPS_NAME": "metricspire-synthetic",
            "METRICSPIRE_APPS_WORKSPACE_ID": "314",
            "METRICSPIRE_APPS_PUBLIC_URL": "https://synthetic-user:synthetic-password@example.com",
            "METRICSPIRE_APPS_PUBLISHER_GROUP_ID": "synthetic-publishers",
            "METRICSPIRE_APPS_LAKEBASE_ENDPOINT": "projects/synthetic/branches/staging/endpoints/primary",
        }
        with patch.dict(os.environ, env, clear=True), patch.object(sys, "argv", ["render.py", str(self.output)]):
            with self.assertRaises(RuntimeError):
                renderer.main()
        self.assertFalse((self.output / "config" / "runtime.json").exists())


if __name__ == "__main__":
    unittest.main()
