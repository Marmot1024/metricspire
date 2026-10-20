import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('builder', Path(__file__).with_name('build_mcp_installer.py'))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)

class OIDCBuilderTest(unittest.TestCase):
    def connection(self):
        return dict(url='https://metrics.example.com/mcp',issuer='https://identity.example.com/realms/demo',
                    client_id='public-mcp',scopes=['metrics','offline_access'])
    def test_public_bundle(self):
        builder.validate_oidc_connection(self.connection())
    def test_reject_secret_or_invalid_bundle(self):
        for field,value in [('client_secret','secret'),('url','http://metrics.example.com/mcp'),
                            ('scopes',['metrics','metrics']),('scopes',['one two']),('client_id',None),
                            ('issuer','https://user:secret@identity.example.com')]:
            with self.subTest(field=field):
                c=self.connection();c[field]=value
                with self.assertRaises(ValueError):builder.validate_oidc_connection(c)

if __name__ == '__main__':unittest.main()
