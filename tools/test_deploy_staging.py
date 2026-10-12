import json
import io
from pathlib import Path
import tempfile
import tarfile
import unittest

from tools.deploy_staging import preserve_config, extract_source, configure_exploration


class PreserveConfigTest(unittest.TestCase):
    def test_repeat_release_preserves_catalog_and_cannot_skip_analysis_acceptance(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'config').mkdir()
            runtime=root/'config/runtime.json'
            runtime.write_text(json.dumps({'authentication':{'databricks_apps':{'tenant':'acceptance'}}}))
            catalog=root/'catalog.json'
            catalog.write_text(json.dumps({'namespace':'acceptance','tenant':'acceptance'}))
            configure_exploration(root,'acceptance',catalog,'warehouse',True)
            original=runtime.read_bytes()
            for path in (catalog,None):
                configure_exploration(root,'acceptance',path,'warehouse',True)
                self.assertEqual(runtime.read_bytes(),original)
                with self.assertRaises(RuntimeError):
                    configure_exploration(root,'acceptance',path,'warehouse',False)
            with self.assertRaises(RuntimeError):
                configure_exploration(root,'acceptance',catalog,'different',True)
            catalog.write_text(json.dumps({'namespace':'acceptance','tenant':'other'}))
            with self.assertRaises(RuntimeError):
                configure_exploration(root,'acceptance',catalog,'warehouse',True)
            self.assertEqual(runtime.read_bytes(),original)

    def test_source_archive_cannot_escape_or_link(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for name, kind in (('../escape',tarfile.REGTYPE), ('link',tarfile.SYMTYPE)):
                archive=root/'source.tar'
                with tarfile.open(archive,'w') as tar:
                    member=tarfile.TarInfo(name);member.type=kind;member.linkname='/etc/passwd'
                    tar.addfile(member,io.BytesIO())
                with self.assertRaises(RuntimeError): extract_source(archive,root/'output')

    def test_preserves_business_online_and_app_configuration_byte_for_byte(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            frozen, package = root / 'frozen', root / 'package'
            (frozen / 'config').mkdir(parents=True); package.mkdir()
            (frozen / 'app.yaml').write_text('env: []\n')
            runtime = dict(online={'data_access':'tenant_shared'}, policies=[{'path':'business.yaml'}])
            (frozen / 'config/runtime.json').write_text(json.dumps(runtime))
            (frozen / 'config/business.yaml').write_text('rules: []\n')
            preserve_config(frozen, package)
            for relative in ('app.yaml','config/runtime.json','config/business.yaml'):
                self.assertEqual((frozen / relative).read_bytes(), (package / relative).read_bytes())

    def test_refuses_symlinks_in_config(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); frozen=root/'frozen'; package=root/'package'
            (frozen/'config').mkdir(parents=True); package.mkdir()
            (frozen/'app.yaml').write_text('env: []')
            (frozen/'config/runtime.json').write_text('{}')
            (frozen/'config/secret.json').symlink_to('/etc/passwd')
            with self.assertRaises(RuntimeError): preserve_config(frozen,package)


if __name__ == '__main__': unittest.main()
