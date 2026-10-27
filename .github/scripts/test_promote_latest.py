import hashlib
import io
import itertools
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch
import urllib.error

from promote_latest import MEDIA_TYPES, Registry, VERSION, promote


DIGEST = 'sha256:' + 'a' * 64
IMAGE = 'omyto/tailhop'


def index(release):
    return {'mediaType': MEDIA_TYPES[0], 'annotations': {VERSION: release},
            'manifests': [{'platform': {'os': 'linux', 'architecture': arch}}
                          for arch in ('amd64', 'arm64')]}


class PromotionTests(unittest.TestCase):
    def registry(self, release='0.2.0', current=None):
        registry = Mock()
        registry.manifest.side_effect = [(index(release), DIGEST), current,
                                         (index(release), DIGEST)]
        return registry

    def test_first_newer_and_equal_releases(self):
        for current in (None, (index('0.1.0'), 'old'), (index('0.2.0'), DIGEST)):
            with self.subTest(current=current):
                run = Mock()
                registry = self.registry(current=current)
                promote(IMAGE, '0.2.0', DIGEST, registry, run)
                run.assert_called_once_with(
                    ['docker', 'buildx', 'imagetools', 'create', '--tag',
                     IMAGE + ':latest', IMAGE + '@' + DIGEST], check=True)
                self.assertEqual(registry.manifest.call_args_list[-1].args, ('latest',))

    def test_old_rerun_never_downgrades(self):
        run = Mock()
        promote(IMAGE, '0.1.0', DIGEST,
                self.registry('0.1.0', (index('0.2.0'), 'newer')), run)
        run.assert_not_called()

    def test_all_serialized_release_orders(self):
        # Concurrent jobs may acquire the shared lock in any order; no execution
        # order, including an old rerun, may decrease the published version.
        for order in itertools.permutations(('0.2.0', '0.10.0', '1.0.0')):
            current = None
            for release in (*order, '0.1.0'):
                run = Mock()
                promote(IMAGE, release, DIGEST, self.registry(release, current), run)
                if run.called:
                    current = (index(release), DIGEST)
            self.assertEqual(current[0]['annotations'][VERSION], '1.0.0')

    def test_invalid_versions_and_digest_do_not_push(self):
        for release in ('0.3.0-alpha.1', '0.3.0-beta.1', '0.3.0-rc.1',
                        '0.3.0+build', 'v0.3.0', '01.2.3', '1.2', ''):
            run, registry = Mock(), Mock()
            with self.subTest(release=release), self.assertRaises(ValueError):
                promote(IMAGE, release, DIGEST, registry, run)
            run.assert_not_called()
            registry.manifest.assert_not_called()
        run = Mock()
        with self.assertRaises(ValueError):
            promote(IMAGE, '0.2.0', 'bad-digest', Mock(), run)
        run.assert_not_called()

    def test_untrusted_metadata_fails_closed(self):
        bad = index('0.2.0')
        del bad['annotations']
        single = index('0.2.0')
        single['manifests'].pop()
        for manifest in (bad, single, index('0.2.0-beta.1'),
                         {'mediaType': 'application/vnd.oci.image.manifest.v1+json'}):
            for candidate in (True, False):
                registry, run = Mock(), Mock()
                registry.manifest.side_effect = [
                    (manifest if candidate else index('0.2.0'), DIGEST),
                    None if candidate else (manifest, DIGEST),
                ]
                with self.subTest(candidate=candidate, manifest=manifest), self.assertRaises(ValueError):
                    promote(IMAGE, '0.2.0', DIGEST, registry, run)
                run.assert_not_called()

    def test_build_mismatch_and_verification_failure(self):
        for manifest, digest in ((index('0.1.0'), DIGEST), (index('0.2.0'), 'different')):
            registry, run = Mock(), Mock()
            registry.manifest.return_value = (manifest, digest)
            with self.assertRaises(ValueError):
                promote(IMAGE, '0.2.0', DIGEST, registry, run)
            run.assert_not_called()
        registry, run = self.registry(), Mock()
        registry.manifest.side_effect = [(index('0.2.0'), DIGEST), None,
                                         (index('0.2.0'), 'wrong-digest')]
        with self.assertRaises(ValueError):
            promote(IMAGE, '0.2.0', DIGEST, registry, run)

    def test_registry_and_push_errors_propagate(self):
        for failed_read in (0, 1):
            registry, run = Mock(), Mock()
            registry.manifest.side_effect = ([(index('0.2.0'), DIGEST)] * failed_read
                                             + [OSError('offline')])
            with self.assertRaises(OSError):
                promote(IMAGE, '0.2.0', DIGEST, registry, run)
            run.assert_not_called()
        run = Mock(side_effect=subprocess.CalledProcessError(1, 'docker'))
        with self.assertRaises(subprocess.CalledProcessError):
            promote(IMAGE, '0.2.0', DIGEST, self.registry(), run)

    @patch('promote_latest.urllib.request.urlopen')
    def test_registry_transport(self, urlopen):
        urlopen.return_value.__enter__.return_value = io.BytesIO(b'{"token":"test-token"}')
        registry = Registry(IMAGE)
        for code in (404, 401, 403, 429, 500):
            urlopen.side_effect = urllib.error.HTTPError('registry', code, '', {}, None)
            if code == 404:
                self.assertIsNone(registry.manifest('latest', missing_ok=True))
            else:
                with self.assertRaises(urllib.error.HTTPError):
                    registry.manifest('latest', missing_ok=True)
            with self.assertRaises(urllib.error.HTTPError):
                registry.manifest('0.2.0')
        urlopen.side_effect = None
        raw = json.dumps(index('0.2.0')).encode()
        urlopen.return_value.__enter__.return_value = io.BytesIO(raw)
        manifest, digest = registry.manifest('latest')
        self.assertEqual(digest, 'sha256:' + hashlib.sha256(raw).hexdigest())
        self.assertEqual(manifest, index('0.2.0'))
        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header('Authorization'), 'Bearer test-token')
        self.assertEqual(urlopen.call_args.kwargs['timeout'], 30)

    def test_workflow_stable_gate_and_promotion_wiring(self):
        workflow = (Path(__file__).resolve().parents[1] / 'workflows/docker-publish.yml').read_text()
        gate = workflow.split('        run: |\n', 1)[1].split('      - uses:', 1)[0]
        gate = '\n'.join(line[10:] for line in gate.splitlines())
        with tempfile.TemporaryDirectory() as tmp:
            for tag, valid in [('v0.1.0', True), ('v0.2.0', True), ('v10.20.30', True),
                               ('v0.3.0-beta.1', False), ('v1.0.0-rc.1', False),
                               ('v1.0.0-alpha', False), ('v01.2.3', False), ('v1.2.3+build', False)]:
                env = dict(os.environ, GITHUB_REF_NAME=tag, GITHUB_OUTPUT=tmp + '/output')
                result = subprocess.run(['bash', '-c', gate], env=env, capture_output=True)
                self.assertEqual(result.returncode == 0, valid, tag)
        latest = workflow.split('  latest:\n', 1)[1]
        self.assertIn('needs: [verify, publish]', latest)
        self.assertIn('group: docker-publish-latest', latest)
        self.assertIn('queue: max', latest)
        self.assertIn('cancel-in-progress: false', latest)
        self.assertIn('IMAGE_DIGEST: ${{ needs.publish.outputs.digest }}', latest)
        self.assertIn('annotations: index:' + VERSION + '=${{ needs.verify.outputs.version }}', workflow)
        self.assertIn('platforms: linux/amd64,linux/arm64', workflow)


if __name__ == '__main__':
    unittest.main()
