import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch
import urllib.error

spec = importlib.util.spec_from_file_location("register_templates", Path(__file__).with_name("register-templates.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ReadTransportTests(unittest.TestCase):
    def test_template_mirror_preserves_published_runner_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'runtime.json'
            mirror = 'http://192.0.2.1:18769/cnjack'
            existing = [{
                'aliases': [f'jcloud-{profile}-v0-0-159'],
                'templateID': f'tpl-{profile}',
                'imageInfo': f'{mirror}/jcloud-cube-{profile}:v0.0.159',
            } for profile in module.PROFILES]
            def read(_opener, request):
                return existing if request.full_url.endswith('/templates') else {'status': 'READY'}
            argv = ['register-templates.py', '--release', 'v0.0.159', '--registry', 'ghcr.io/cnjack',
                    '--template-registry', mirror, '--output', str(output)]
            with patch('sys.argv', argv), patch.object(module, 'read_json', side_effect=read), \
                    patch('sys.stdout', new_callable=io.StringIO), patch.object(module.subprocess, 'run') as run:
                module.main()
            run.assert_not_called()
            config = json.loads(output.read_text())['data']
            self.assertEqual(config['RUNNER_IMAGE'], 'ghcr.io/cnjack/jcloud-runner:v0.0.159')
            self.assertTrue(all(image.startswith('ghcr.io/cnjack/') for image in json.loads(config['RUNNER_PROFILES_JSON']).values()))
            receipt = json.loads((Path(directory) / 'template-default-v0.0.159.generated.json').read_text())
            self.assertEqual(receipt['image'], mirror + '/jcloud-cube-default:v0.0.159')

    def test_invalid_wait_timeout_fails_before_network_access(self):
        with patch("sys.argv", ["register-templates.py", "--release", "v0.0.159", "--wait-timeout", "0"]), \
                patch.object(module.urllib.request, "build_opener") as build_opener, \
                patch("sys.stderr", new_callable=io.StringIO), self.assertRaises(SystemExit) as error:
            module.main()
        self.assertEqual(error.exception.code, 2)
        build_opener.assert_not_called()

    def test_transport_failure_retries_the_same_read(self):
        opener = Mock()
        opener.open.side_effect = [urllib.error.URLError(TimeoutError()), io.BytesIO(b'{"status":"READY"}')]
        request = object()
        with patch.object(module.time, "sleep"):
            self.assertEqual(module.read_json(opener, request), {"status": "READY"})
        self.assertEqual(opener.open.call_count, 2)
        self.assertTrue(all(call.args == (request,) for call in opener.open.call_args_list))

    def test_http_status_is_preserved_for_template_pending_logic(self):
        opener = Mock()
        opener.open.side_effect = urllib.error.HTTPError("http://fixture", 404, "pending", {}, None)
        with self.assertRaises(urllib.error.HTTPError):
            module.read_json(opener, object())
        self.assertEqual(opener.open.call_count, 1)

    def test_transport_failure_remains_visible_after_bound(self):
        opener = Mock()
        opener.open.side_effect = TimeoutError()
        with patch.object(module.time, "sleep"), self.assertRaises(TimeoutError):
            module.read_json(opener, object())
        self.assertEqual(opener.open.call_count, 3)


if __name__ == "__main__":
    unittest.main()
