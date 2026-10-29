"""Run the installer with isolated files and fake account/systemd commands.

No host accounts, installation paths, or services are accessed by these tests.
"""

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
FAKE_TOOL = r'''#!/usr/bin/python3
import json, os, pathlib, shutil, subprocess, sys, tempfile
root = pathlib.Path(os.environ['INSTALLER_TEST_ROOT'])
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with (root / 'calls').open('a') as log:
    log.write(json.dumps([tool, *args]) + '\n')
if os.environ.get('FAIL_TOOL') == tool:
    sys.exit(1)
accounts = root / 'accounts.json'
data = json.loads(accounts.read_text())
def target(value):
    for prefix in ('/usr/local/bin', '/etc/systemd/system', '/usr/local/share/doc/tailhop'):
        if value == prefix or value.startswith(prefix + '/'):
            value = str(root / 'host') + value
            break
    path = pathlib.Path(value)
    assert path.is_relative_to(root), 'Attempt to write outside test sandbox'
    return path
if tool == 'uname':
    print(os.environ.get('FAKE_ARCH', 'x86_64') if args == ['-m'] else os.environ.get('FAKE_OS', 'Linux'))
elif tool == 'id':
    print(os.environ.get('FAKE_UID', '0'))
elif tool == 'getent':
    entry = data.get(args[0])
    if not entry:
        sys.exit(2)
    print(entry)
elif tool == 'groupadd':
    data['group'] = 'tailhop:x:987:'
    accounts.write_text(json.dumps(data))
elif tool == 'useradd':
    data['passwd'] = 'tailhop:x:987:987::/var/lib/tailhop:' + args[-2]
    accounts.write_text(json.dumps(data))
elif tool == 'install':
    assert args[args.index('-o') + 1] == 'root'
    assert args[args.index('-g') + 1] == 'root'
    mode = int(args[args.index('-m') + 1], 8)
    if '-d' in args:
        for value in args[7:]:
            path = target(value)
            path.mkdir(parents=True, exist_ok=True)
            path.chmod(mode)
    else:
        if os.environ.get('FAIL_UNIT_COPY') and args[-2].endswith('tailhop.service'):
            sys.exit(1)
        path = target(args[-1])
        shutil.copyfile(args[-2], path)
        path.chmod(mode)
elif tool == 'mktemp':
    if args == ['-d']:
        name = tempfile.mkdtemp(prefix='download-', dir=root)
    else:
        path = target(args[0])
        fd, name = tempfile.mkstemp(prefix='.tailhop.', dir=path.parent)
        os.close(fd)
    print(name)
elif tool == 'mv':
    target(args[-2]).replace(target(args[-1]))
elif tool == 'rm':
    path = target(args[-1])
    if path.is_dir():
        shutil.rmtree(path)
    else:
        path.unlink(missing_ok=True)
elif tool == 'curl':
    assert args[0] == '-q'
    assert args[args.index('--proto') + 1] == '=https'
    assert args[args.index('--proto-redir') + 1] == '=https'
    if args[-1].startswith('https://api.github.com/repos/omyto/tailhop/releases/'):
        source = root / 'release.json'
    else:
        assert args[-1].startswith('https://github.com/omyto/tailhop/releases/download/')
        if os.environ.get('FAIL_ARCHIVE_DOWNLOAD'):
            sys.exit(1)
        source = root / 'release.tar.gz'
    shutil.copyfile(source, target(args[args.index('-o') + 1]))
elif tool == 'sudo':
    sys.exit(subprocess.run(args, env=dict(os.environ, FAKE_UID='0')).returncode)
elif tool == 'timeout':
    assert args[:2] == ['-k', '1s']
    if os.environ.get('LOGIN_TIMEOUT') and args[2] == '30s':
        sys.exit(124)
    sys.exit(subprocess.run(args[3:]).returncode)
elif tool == 'sleep':
    pass
elif tool == 'journalctl':
    assert '_SYSTEMD_UNIT=tailhop.service' in args
    assert '_SYSTEMD_INVOCATION_ID=' + 'a' * 32 in args
    count_path = root / 'journal-count'
    count = int(count_path.read_text()) if count_path.exists() else 0
    count_path.write_text(str(count + 1))
    if count >= int(os.environ.get('LOGIN_DELAY_POLLS', '0')):
        print(os.environ.get('STARTUP_LOGS', '{"msg":"tsnet","message":"AuthLoop: state is Running; done"}'))
    else:
        print(os.environ.get('EARLY_STARTUP_LOGS', ''))
elif tool == 'systemctl':
    if os.environ.get('FAIL_RESTART') and args[0] == 'restart':
        sys.exit(1)
    if args[0] == 'show' and '--property=InvocationID' in args:
        print(os.environ.get('INVOCATION_ID', 'a' * 32))
else:
    sys.exit('Unexpected fake tool: ' + tool)
'''


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.payload = self.root / 'payload with spaces'
        self.payload.mkdir()
        shutil.copy(ROOT / 'install.sh', self.payload)
        for name in ('LICENSE', 'THIRD_PARTY_NOTICES'):
            shutil.copy(ROOT / name, self.payload)
        shutil.copytree(ROOT / 'packaging', self.payload / 'packaging')
        (self.payload / 'tailhop').write_bytes(b'new binary')
        self.host = self.root / 'host'
        self.host.mkdir()
        self.accounts = self.root / 'accounts.json'
        self.accounts.write_text('{}')
        tools = self.root / 'tools'
        tools.mkdir()
        for name in ('uname', 'id', 'getent', 'groupadd', 'useradd', 'install',
                     'mktemp', 'mv', 'rm', 'systemctl', 'nologin', 'curl', 'sudo',
                     'journalctl', 'timeout', 'sleep'):
            path = tools / name
            path.write_text(FAKE_TOOL)
            path.chmod(0o755)
        self.env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'],
                        INSTALLER_TEST_ROOT=str(self.root))

    def run_installer(self, *args, **env):
        return subprocess.run(['sh', str(self.payload / 'install.sh'), *args],
                              cwd=self.root, env=dict(self.env, **env),
                              capture_output=True, text=True)

    def calls(self, tool):
        log = self.root / 'calls'
        return [args[1:] for line in log.read_text().splitlines()
                if (args := json.loads(line))[0] == tool] if log.exists() else []

    def test_install_only_and_repeat_preserve_identity_and_config(self):
        for relative in ('etc/tailhop/tailhop.env', 'var/lib/tailhop/tailscaled.state',
                         'etc/systemd/system/tailhop.service.d/custom.conf'):
            path = self.host / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b'private existing content')
            path.chmod(0o600)
        binary = self.host / 'usr/local/bin/tailhop'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'old binary')
        old_inode = self.root / 'running-binary'
        os.link(binary, old_inode)
        for _ in range(2):
            result = self.run_installer('--no-start')
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(binary.read_bytes(), b'new binary')
        self.assertEqual(binary.stat().st_mode & 0o777, 0o755)
        self.assertEqual(old_inode.read_bytes(), b'old binary')
        unit = self.host / 'etc/systemd/system/tailhop.service'
        self.assertEqual(unit.read_bytes(), (ROOT / 'packaging/systemd/tailhop.service').read_bytes())
        self.assertEqual(unit.stat().st_mode & 0o777, 0o644)
        for name in ('LICENSE', 'THIRD_PARTY_NOTICES'):
            self.assertEqual((self.host / 'usr/local/share/doc/tailhop' / name).read_bytes(),
                             (ROOT / name).read_bytes())
        for relative in ('etc/tailhop/tailhop.env', 'var/lib/tailhop/tailscaled.state',
                         'etc/systemd/system/tailhop.service.d/custom.conf'):
            path = self.host / relative
            self.assertEqual(path.read_bytes(), b'private existing content')
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(len(self.calls('groupadd')), 1)
        self.assertEqual(len(self.calls('useradd')), 1)
        self.assertFalse(any(call[0] in ('enable', 'restart', 'start', 'stop')
                             for call in self.calls('systemctl')))
        self.assertEqual(self.calls('journalctl'), [])
        self.assertEqual(self.calls('timeout'), [])
        self.assertFalse(list(self.host.rglob('.tailhop.*')))

    def test_default_and_explicit_start_enable_and_restart_after_reload(self):
        for args in ((), ('--start',)):
            with self.subTest(args=args):
                result = self.run_installer(*args)
                self.assertEqual(result.returncode, 0, result.stderr)
                mutations = [call for call in self.calls('systemctl')
                             if call[0] in ('daemon-reload', 'enable', 'restart')]
                self.assertEqual(mutations[-3:],
                                 [['daemon-reload'], ['enable', 'tailhop'], ['restart', 'tailhop']])

    def test_preflight_failures_do_not_modify_installation(self):
        for env in ({'FAKE_UID': '1000'}, {'FAKE_OS': 'Darwin'}, {'FAIL_TOOL': 'systemctl'}):
            with self.subTest(env=env):
                self.assertNotEqual(self.run_installer(**env).returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('useradd'), [])

    def test_help_and_unknown_option(self):
        result = self.run_installer('--help', FAKE_UID='1000')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Usage:', result.stdout)
        self.assertNotEqual(self.run_installer('--bogus').returncode, 0)
        self.assertEqual(self.calls('systemctl'), [])

    def test_missing_or_symlinked_payload_is_rejected(self):
        binary = self.payload / 'tailhop'
        binary.unlink()
        for symlink in (False, True):
            with self.subTest(symlink=symlink):
                if symlink:
                    binary.symlink_to(ROOT / 'LICENSE')
                self.assertNotEqual(self.run_installer().returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('useradd'), [])

    def test_incompatible_account_or_group_is_rejected(self):
        cases = [
            {'group': 'tailhop:x:0:'},
            {'group': 'tailhop:x:987:someone'},
            {'group': 'tailhop:x:987:', 'passwd': 'tailhop:x:0:987::/var/lib/tailhop:/bin/false'},
            {'group': 'tailhop:x:987:', 'passwd': 'tailhop:x:987:987::/home/tailhop:/bin/bash'},
            {'group': 'tailhop:x:987:', 'passwd': 'tailhop:x:987:987::/var/lib/tailhop:/bin/bash'},
        ]
        for data in cases:
            with self.subTest(data=data):
                self.accounts.write_text(json.dumps(data))
                self.assertNotEqual(self.run_installer().returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('useradd'), [])

    def test_copy_failure_cleans_staging_before_replacement(self):
        binary = self.host / 'usr/local/bin/tailhop'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'old binary')
        result = self.run_installer('--start', FAIL_UNIT_COPY='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(binary.read_bytes(), b'old binary')
        self.assertFalse(list(self.host.rglob('.tailhop.*')))
        self.assertEqual(self.calls('useradd'), [])
        self.assertNotIn(['restart', 'tailhop'], self.calls('systemctl'))

    def test_service_failure_is_reported(self):
        self.assertNotEqual(self.run_installer('--start', FAIL_RESTART='1').returncode, 0)
        self.assertEqual(self.calls('journalctl'), [])

    def test_login_url_is_printed_after_delayed_startup(self):
        self.prepare_release()
        url = 'https://login.tailscale.com/a/fixture-login'
        early = json.dumps({'msg': 'proxy listening', 'port': 8080})
        logs = '\n'.join([
            json.dumps({'msg': 'request', 'url': 'https://login.tailscale.com/a/unrelated'}),
            json.dumps({'msg': 'tsnet', 'message': 'To start this tsnet server, go to: ' + url,
                        'unrelated': 'private value that must not be printed'}),
        ])
        result = self.run_streamed(FAKE_UID='1000', STARTUP_LOGS=logs,
                                   EARLY_STARTUP_LOGS=early, LOGIN_DELAY_POLLS='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count(url), 1)
        self.assertIn('keep it private', result.stdout)
        self.assertNotIn('/a/unrelated', result.stdout)
        self.assertNotIn('private value', result.stdout)
        self.assertEqual(self.calls('sleep'), [['1']])
        self.assertEqual(len(self.calls('journalctl')), 2)
        for call in self.calls('journalctl'):
            self.assertIn('_SYSTEMD_INVOCATION_ID=' + 'a' * 32, call)

    def test_running_state_needs_no_login_url(self):
        url = 'https://login.tailscale.com/a/already-used'
        logs = '\n'.join([
            json.dumps({'msg': 'tsnet', 'message': 'go to: ' + url}),
            json.dumps({'msg': 'tsnet', 'message': 'AuthLoop: state is Running; done'}),
        ])
        result = self.run_installer(STARTUP_LOGS=logs)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('no browser login is needed', result.stdout)
        self.assertNotIn(url, result.stdout)
        self.assertEqual(self.calls('sleep'), [])

    def test_login_discovery_timeout_or_journal_failure_is_not_install_failure(self):
        for env in ({'LOGIN_TIMEOUT': '1'}, {'FAIL_TOOL': 'journalctl'}):
            with self.subTest(env=env):
                result = self.run_installer(**env)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('sudo journalctl -u tailhop -f', result.stdout)
                self.assertIn('systemctl status tailhop', result.stdout)
                self.assertNotIn('no browser login is needed', result.stdout)
        self.assertTrue(any(call[:3] == ['-k', '1s', '30s'] for call in self.calls('timeout')))

    def test_invalid_invocation_id_never_reads_old_logs(self):
        result = self.run_installer(INVOCATION_ID='')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('sudo journalctl -u tailhop -f', result.stdout)
        self.assertEqual(self.calls('journalctl'), [])

    def prepare_release(self, arch='amd64', extra=None, replacement=None):
        materials = ['tailhop', 'install.sh', 'LICENSE', 'THIRD_PARTY_NOTICES', 'INSTALL.md',
                     'packaging/systemd/tailhop.service', 'packaging/systemd/tailhop.env.example']
        archive = self.root / 'release.tar.gz'
        with tarfile.open(archive, 'w:gz') as payload:
            for name in materials:
                if replacement is not None and replacement.name == name:
                    payload.addfile(replacement)
                    continue
                source = self.payload / name if (self.payload / name).exists() else ROOT / name
                payload.add(source, arcname=name, recursive=False)
            if extra is not None:
                payload.addfile(extra, io.BytesIO(b'x') if extra.isfile() else None)
        self.release = dict(tag_name='v0.2.0', draft=False, prerelease=False, assets=[{
            'name': f'tailhop_0.2.0_linux_{arch}.tar.gz', 'state': 'uploaded',
            'digest': 'sha256:' + hashlib.sha256(archive.read_bytes()).hexdigest(),
        }])
        self.save_metadata()

    def save_metadata(self):
        (self.root / 'release.json').write_text(json.dumps(self.release))

    def run_streamed(self, *args, **env):
        return subprocess.run(['sh', '-s', '--', *args], input=(ROOT / 'install.sh').read_text(),
                              cwd=self.root, env=dict(self.env, **env),
                              capture_output=True, text=True)

    def test_streamed_latest_download_install_and_sudo(self):
        self.prepare_release()
        result = self.run_streamed(FAKE_UID='1000')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.host / 'usr/local/bin/tailhop').read_bytes(), b'new binary')
        self.assertEqual(self.calls('curl')[0][-1],
                         'https://api.github.com/repos/omyto/tailhop/releases/latest')
        self.assertEqual(self.calls('curl')[1][-1],
                         'https://github.com/omyto/tailhop/releases/download/v0.2.0/tailhop_0.2.0_linux_amd64.tar.gz')
        self.assertEqual(self.calls('sudo')[0][0], 'sh')
        self.assertEqual(self.calls('sudo')[0][-1], '--start')
        self.assertIn(['restart', 'tailhop'], self.calls('systemctl'))
        self.assertFalse(list(self.root.glob('download-*')))

    def test_download_and_install_without_python_in_path(self):
        self.prepare_release()
        tools = self.root / 'tools'
        for name in ('sh', 'awk', 'tar', 'gzip', 'sha256sum', 'sort', 'cmp', 'mkdir', 'cat', 'dirname'):
            (tools / name).symlink_to(shutil.which(name))
        self.env['PATH'] = str(tools)
        for name in ('python', 'python3', 'jq'):
            self.assertIsNone(shutil.which(name, path=self.env['PATH']))
        result = self.run_streamed(FAKE_UID='1000')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.host / 'usr/local/bin/tailhop').read_bytes(), b'new binary')
        self.assertIn(['restart', 'tailhop'], self.calls('systemctl'))
        # Exercise another common POSIX awk implementation when available.
        if busybox := shutil.which('busybox'):
            (tools / 'awk').unlink()
            (tools / 'awk').symlink_to(busybox)
            result = self.run_streamed(FAKE_UID='1000')
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_saved_script_download_pinned_arm64_release_without_start(self):
        self.prepare_release(arch='arm64')
        result = self.run_installer('--download', '--version', 'v0.2.0', '--no-start', FAKE_ARCH='aarch64')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls('curl')[0][-1],
                         'https://api.github.com/repos/omyto/tailhop/releases/tags/v0.2.0')
        self.assertTrue(self.calls('curl')[1][-1].endswith('_arm64.tar.gz'))
        self.assertFalse(any(call[0] in ('enable', 'restart', 'start', 'stop')
                             for call in self.calls('systemctl')))
        self.assertEqual(self.calls('sudo'), [])

    def test_streamed_shell_path_does_not_select_local_payload(self):
        self.prepare_release()
        (self.root / 'sh').write_text('unrelated file in the working directory')
        for shell in ('sh', '/bin/sh'):
            with self.subTest(shell=shell):
                result = subprocess.run([shell, '-s'], input=(ROOT / 'install.sh').read_text(),
                                        cwd=self.root, env=self.env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.calls('curl')), 4)

    def test_corrupt_archive_cannot_install(self):
        self.prepare_release()
        (self.root / 'release.tar.gz').write_bytes(b'corrupt archive')
        result = self.run_streamed('--start')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('SHA-256 mismatch', result.stderr)
        self.assertEqual(list(self.host.iterdir()), [])
        self.assertEqual(self.calls('sudo'), [])
        self.assertFalse(list(self.root.glob('download-*')))

    def test_invalid_release_metadata_cannot_install(self):
        mutations = [
            lambda r: r.update(draft=True),
            lambda r: r.update(prerelease=True),
            lambda r: r.update(tag_name='../../bad'),
            lambda r: r.update(assets=[]),
            lambda r: r['assets'].append(r['assets'][0].copy()),
            lambda r: r['assets'][0].update(digest=None),
            lambda r: r['assets'][0].update(digest='sha256:invalid'),
            lambda r: r['assets'][0].update(state='new'),
            lambda r: r.update(draft='false'),
            lambda r: r.update(prerelease=None),
            lambda r: r.update(assets={'1': r['assets'][0]}),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.prepare_release()
                mutate(self.release)
                self.save_metadata()
                self.assertNotEqual(self.run_streamed('--start').returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('sudo'), [])
                self.assertFalse(list(self.root.glob('download-*')))

    def test_metadata_structure_and_string_escapes(self):
        self.prepare_release()
        self.release['body'] = 'Text: "draft": true, {"digest": "sha256:wrong"} \\ \n \t café'
        self.release['unused'] = [None, True, False, -1.5e10, {'nested': [1, 'a/b']}]
        self.release['assets'].insert(0, dict(name='different-asset', state='uploaded',
                                             digest='sha256:' + '0' * 64))
        for indent in (None, 2):
            with self.subTest(indent=indent):
                (self.root / 'release.json').write_text(json.dumps(self.release, indent=indent, ensure_ascii=False))
                result = self.run_streamed()
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_malformed_and_duplicate_json_cannot_install(self):
        self.prepare_release()
        valid = json.dumps(self.release)
        invalid = [valid[:-1], valid + ' trailing', valid[:-1] + ',}',
                   valid.replace('"draft": false', '"draft": false, "draft": true'),
                   valid.replace('"draft": false', '"draft": false, "draft": false'),
                   valid.replace('"draft":', '"dra\\u0066t":'),
                   valid.replace('"draft": false', '"draft": falsex'),
                   valid[:-1] + ', "unused": "bad\\q"}',
                   valid[:-1] + ', "unused": 01}',
                   valid[:-1] + ', "unused": ' + '[' * 70 + '0' + ']' * 70 + '}']
        for metadata in invalid:
            with self.subTest(metadata=metadata):
                (self.root / 'release.json').write_text(metadata)
                self.assertNotEqual(self.run_streamed('--start').returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('sudo'), [])

    def test_archive_links_extra_files_and_traversal_cannot_install(self):
        link = tarfile.TarInfo('link')
        link.type, link.linkname = tarfile.SYMTYPE, '/etc/shadow'
        for extra in (link, tarfile.TarInfo('../escaped'), tarfile.TarInfo('tailhop')):
            with self.subTest(name=extra.name):
                self.prepare_release(extra=extra)
                result = self.run_streamed('--start')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('Unexpected archive layout', result.stderr)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertFalse((self.root / 'escaped').exists())

    def test_archive_special_entries_under_expected_names_cannot_install(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.FIFOTYPE, tarfile.CHRTYPE):
            with self.subTest(kind=kind):
                replacement = tarfile.TarInfo('install.sh')
                replacement.type = kind
                replacement.linkname = 'tailhop'
                self.prepare_release(replacement=replacement)
                result = self.run_streamed('--start')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('Unexpected archive layout', result.stderr)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('sudo'), [])

    def test_download_failures_do_not_install(self):
        self.prepare_release()
        for env in ({'FAIL_TOOL': 'curl'}, {'FAIL_ARCHIVE_DOWNLOAD': '1'}, {'FAKE_ARCH': 'armv7l'}):
            with self.subTest(env=env):
                self.assertNotEqual(self.run_streamed('--start', **env).returncode, 0)
                self.assertEqual(list(self.host.iterdir()), [])
                self.assertEqual(self.calls('sudo'), [])
                self.assertFalse(list(self.root.glob('download-*')))

    def test_invalid_or_missing_version_is_rejected_before_download(self):
        for args in (('--version',), ('--version', '../bad'), ('--version', 'v01.2.0')):
            with self.subTest(args=args):
                self.assertNotEqual(self.run_streamed(*args).returncode, 0)
                self.assertEqual(self.calls('curl'), [])

    def test_pinned_release_must_match_requested_tag(self):
        self.prepare_release()
        self.assertNotEqual(self.run_streamed('--version', 'v0.3.0').returncode, 0)
        self.assertEqual(list(self.host.iterdir()), [])

    def test_truncated_stream_does_not_install(self):
        script = (ROOT / 'install.sh').read_text().rsplit('main "$@"', 1)[0]
        result = subprocess.run(['sh'], input=script, cwd=self.root, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls('curl'), [])
        self.assertEqual(self.calls('systemctl'), [])


if __name__ == '__main__':
    unittest.main()
