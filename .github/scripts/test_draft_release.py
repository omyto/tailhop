import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock

from draft_release import API, digest, update

TAG = 'v0.1.0'
COMMIT = 'a' * 40
NAMES = ['tailhop_0.1.0_linux_amd64.tar.gz', 'tailhop_0.1.0_linux_arm64.tar.gz']


class FakeAPI:
    root = '/repos/test/tailhop'

    def __init__(self):
        self.releases = []
        self.assets = {}
        self.contents = {}
        self.calls = []
        self.next_id = 10
        self.tag_commit = COMMIT
        self.uploads = 0
        self.fail_upload = None
        self.corrupt_upload = False
        self.publish_after_upload = False
        self.fail_rename = None
        self.fail_cleanup = False

    def release(self):
        release = {'id': 1, 'tag_name': TAG, 'draft': True,
                   'upload_url': 'https://uploads.github.com/assets{?name,label}'}
        self.releases.append(release)
        return release

    def asset(self, name, content):
        self.next_id += 1
        item = {'id': self.next_id, 'name': name, 'size': len(content),
                'digest': digest(content), 'state': 'uploaded'}
        self.assets[item['id']] = item
        self.contents[item['id']] = content
        return copy.deepcopy(item)

    def list(self, path):
        if path.endswith('/releases'):
            return copy.deepcopy(self.releases)
        return copy.deepcopy(list(self.assets.values()))

    def request(self, method, path, data=None, content=None):
        self.calls.append((method, path, data))
        if method == 'GET' and '/git/ref/' in path:
            return {'object': {'type': 'commit', 'sha': self.tag_commit}}
        if method == 'GET':
            return copy.deepcopy(self.releases[0])
        if path.endswith('/releases'):
            assert data['draft'] is True and data['tag_name'] == TAG
            return copy.deepcopy(self.release())
        if path.startswith('https://uploads.github.com/'):
            from urllib.parse import parse_qs, urlparse
            self.uploads += 1
            if self.uploads == self.fail_upload:
                raise RuntimeError('Upload failed: HTTP 503')
            name = parse_qs(urlparse(path).query)['name'][0]
            item = self.asset(name, content)
            if self.corrupt_upload:
                item['digest'] = 'sha256:incorrect'
            if self.publish_after_upload:
                self.releases[0]['draft'] = False
            return item
        asset_id = int(path.rsplit('/', 1)[1])
        if method == 'PATCH':
            if data['name'] == self.fail_rename:
                self.fail_rename = None
                raise RuntimeError('Rename failed')
            assert not any(a['name'] == data['name'] and a['id'] != asset_id for a in self.assets.values())
            self.assets[asset_id]['name'] = data['name']
            return copy.deepcopy(self.assets[asset_id])
        assert method == 'DELETE'
        if self.fail_cleanup:
            self.fail_cleanup = False
            raise RuntimeError('Cleanup failed')
        del self.assets[asset_id]
        del self.contents[asset_id]


class DraftReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        for name in NAMES[:2]:
            (self.directory / name).write_bytes(name.encode())
        manifest = ''.join(f'{digest((self.directory / name).read_bytes())[7:]}  {name}\n' for name in NAMES[:2])
        (self.directory / 'archive-sha256.txt').write_text(manifest)
        self.api = FakeAPI()

    def run_update(self):
        update(self.api, TAG, '0.1.0', COMMIT, self.directory)

    def old_assets(self):
        self.api.release()
        return {name: self.api.asset(name, b'old ' + name.encode()) for name in NAMES}

    def assert_complete(self):
        actual = {a['name']: self.api.contents[a['id']] for a in self.api.assets.values()}
        self.assertEqual(actual, {name: (self.directory / name).read_bytes() for name in NAMES})
        self.assertTrue(self.api.releases[0]['draft'])

    def test_new_draft_and_identical_rerun(self):
        self.run_update()
        self.assert_complete()
        before = list(self.api.calls)
        self.run_update()
        self.assert_complete()
        self.assertEqual(len(self.api.releases), 1)
        self.assertTrue(all(method == 'GET' for method, _, _ in self.api.calls[len(before):]))

    def test_changed_assets_uploaded_before_old_assets_change(self):
        old = self.old_assets()
        self.api.asset('operator-notes.txt', b'keep')
        self.run_update()
        methods = [method for method, _, _ in self.api.calls]
        self.assertEqual(methods[:methods.index('PATCH')].count('POST'), 2)
        self.assertTrue(all(a['id'] not in self.api.assets for a in old.values()))
        self.assertTrue(any(a['name'] == 'operator-notes.txt' for a in self.api.assets.values()))
        self.assertEqual(len(self.api.releases), 1)

    def test_upload_failure_preserves_all_old_assets_and_rerun_recovers(self):
        old = self.old_assets()
        self.api.fail_upload = 2
        with self.assertRaisesRegex(RuntimeError, 'Upload failed'):
            self.run_update()
        self.assertTrue(all(self.api.assets[a['id']]['name'] == name for name, a in old.items()))
        self.assertFalse(any(method in ('PATCH', 'DELETE') for method, _, _ in self.api.calls))
        self.api.fail_upload = None
        self.run_update()
        self.assert_complete()

    def test_corrupt_upload_does_not_replace_old_assets(self):
        old = self.old_assets()
        self.api.corrupt_upload = True
        with self.assertRaisesRegex(RuntimeError, 'Upload verification failed'):
            self.run_update()
        self.assertTrue(all(self.api.assets[a['id']]['name'] == name for name, a in old.items()))

    def test_promotion_failure_rolls_back_without_deleting_bytes(self):
        old = self.old_assets()
        self.api.fail_rename = NAMES[1]
        with self.assertRaisesRegex(RuntimeError, 'Rename failed'):
            self.run_update()
        self.assertTrue(all(self.api.assets[a['id']]['name'] == name for name, a in old.items()))
        self.assertFalse(any(method == 'DELETE' for method, _, _ in self.api.calls))
        self.run_update()
        self.assert_complete()

    def test_cleanup_failure_is_reported_and_rerun_finishes(self):
        self.old_assets()
        self.api.fail_cleanup = True
        with self.assertRaisesRegex(RuntimeError, 'Cleanup failed'):
            self.run_update()
        self.run_update()
        self.assert_complete()

    def test_published_release_is_never_changed(self):
        self.old_assets()
        self.api.releases[0]['draft'] = False
        with self.assertRaisesRegex(RuntimeError, 'published'):
            self.run_update()
        self.assertTrue(all(method == 'GET' for method, _, _ in self.api.calls))

    def test_publication_during_upload_stops_before_renames(self):
        old = self.old_assets()
        self.api.publish_after_upload = True
        with self.assertRaisesRegex(RuntimeError, 'no longer a mutable draft'):
            self.run_update()
        self.assertTrue(all(self.api.assets[a['id']]['name'] == name for name, a in old.items()))
        self.assertFalse(any(method in ('PATCH', 'DELETE') for method, _, _ in self.api.calls))

    def test_duplicate_drafts_fail_closed(self):
        self.api.release()
        self.api.release()
        with self.assertRaisesRegex(RuntimeError, 'Multiple drafts'):
            self.run_update()
        self.assertTrue(all(method == 'GET' for method, _, _ in self.api.calls))

    def test_changed_tag_fails_before_release_creation(self):
        self.api.tag_commit = 'b' * 40
        with self.assertRaisesRegex(RuntimeError, 'built commit'):
            self.run_update()
        self.assertEqual(self.api.releases, [])

    def test_bad_checksums_fail_before_any_api_call(self):
        (self.directory / 'archive-sha256.txt').write_text('bad')
        with self.assertRaisesRegex(RuntimeError, 'archive-sha256'):
            self.run_update()
        self.assertEqual(self.api.calls, [])

    def test_corrupted_archive_bytes_fail_independent_digest_check(self):
        for name in NAMES:
            with self.subTest(archive=name):
                path = self.directory / name
                original = path.read_bytes()
                path.write_bytes(original + b'corruption')
                with self.assertRaisesRegex(RuntimeError, 'archive-sha256'):
                    self.run_update()
                self.assertEqual(self.api.calls, [])
                path.write_bytes(original)

    def test_missing_asset_is_added_without_replacing_identical_assets(self):
        self.api.release()
        originals = [self.api.asset(name, (self.directory / name).read_bytes()) for name in NAMES[:1]]
        self.run_update()
        self.assert_complete()
        self.assertEqual(self.api.uploads, 1)
        self.assertTrue(all(a['id'] in self.api.assets for a in originals))

    def test_failed_lookup_does_not_create_a_release(self):
        self.api.list = Mock(side_effect=RuntimeError('API unavailable'))
        with self.assertRaisesRegex(RuntimeError, 'API unavailable'):
            self.run_update()
        self.assertFalse(any(method != 'GET' for method, _, _ in self.api.calls))

    def test_uncertain_rename_keeps_backups_and_rerun_recovers(self):
        old = self.old_assets()
        original = self.api.request
        failed = False

        def uncertain(method, path, data=None, content=None):
            nonlocal failed
            result = original(method, path, data, content)
            if method == 'PATCH' and not failed:
                failed = True
                raise RuntimeError('Connection lost after server applied rename')
            return result

        self.api.request = uncertain
        with self.assertRaisesRegex(RuntimeError, 'Connection lost'):
            self.run_update()
        self.assertTrue(all(a['id'] in self.api.assets for a in old.values()))
        self.run_update()
        self.assert_complete()

    def test_annotated_tag_is_resolved_to_built_commit(self):
        original = self.api.request

        def annotated(method, path, data=None, content=None):
            if '/git/ref/' in path:
                return {'object': {'type': 'tag', 'sha': 'b' * 40}}
            if '/git/tags/' in path:
                return {'object': {'type': 'commit', 'sha': COMMIT}}
            return original(method, path, data, content)

        self.api.request = annotated
        self.run_update()
        self.assert_complete()

    def legacy_asset(self):
        manifest = ''.join(f"{self.api.assets[a]['digest'][7:]}  {name}\n"
                           for name in NAMES for a in self.api.assets
                           if self.api.assets[a]['name'] == name)
        return self.api.asset('checksums.txt', manifest.encode())

    def test_known_legacy_manifest_removed_after_verified_replacement(self):
        self.old_assets()
        legacy = self.legacy_asset()
        self.run_update()
        self.assertNotIn(legacy['id'], self.api.assets)
        self.assert_complete()

    def test_legacy_manifest_retained_on_upload_failure(self):
        self.old_assets()
        legacy = self.legacy_asset()
        self.api.fail_upload = 2
        with self.assertRaisesRegex(RuntimeError, 'Upload failed'):
            self.run_update()
        self.assertEqual(self.api.assets[legacy['id']]['name'], 'checksums.txt')
        self.api.fail_upload = None
        self.run_update()
        self.assert_complete()

    def test_unrecognized_checksum_attachment_is_preserved(self):
        self.old_assets()
        legacy = self.api.asset('checksums.txt', b'operator-owned content')
        self.run_update()
        self.assertEqual(self.api.assets[legacy['id']]['name'], 'checksums.txt')

    def test_interrupted_legacy_cleanup_recovers_using_archive_backups(self):
        self.old_assets()
        self.legacy_asset()
        original = self.api.request
        failed = False

        def interrupt(method, path, data=None, content=None):
            nonlocal failed
            if method == 'PATCH' and data['name'].endswith('-checksums.txt') and not failed:
                failed = True
                raise RuntimeError('Legacy rename failed')
            return original(method, path, data, content)

        self.api.request = interrupt
        with self.assertRaisesRegex(RuntimeError, 'Legacy rename failed'):
            self.run_update()
        self.run_update()
        self.assert_complete()

    def test_published_release_keeps_legacy_manifest(self):
        self.old_assets()
        legacy = self.legacy_asset()
        self.api.releases[0]['draft'] = False
        with self.assertRaisesRegex(RuntimeError, 'published'):
            self.run_update()
        self.assertIn(legacy['id'], self.api.assets)

    def test_api_pagination(self):
        api = API.__new__(API)
        api.request = Mock(side_effect=[[{'id': i} for i in range(100)], [{'id': 100}]])
        self.assertEqual(len(api.list('/releases')), 101)
        self.assertIn('page=2', api.request.call_args.args[1])


if __name__ == '__main__':
    unittest.main()
