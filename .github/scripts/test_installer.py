"""Run the installer with isolated files and fake account/systemd commands.

No host accounts, installation paths, or services are accessed by these tests.
"""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
FAKE_TOOL = r'''#!/usr/bin/python3
import json, os, pathlib, shutil, sys, tempfile
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
    for prefix in ('/usr/local/bin', '/etc/systemd/system'):
        if value == prefix or value.startswith(prefix + '/'):
            value = str(root / 'host') + value
            break
    path = pathlib.Path(value)
    assert path.is_relative_to(root), 'Attempt to write outside test sandbox'
    return path
if tool == 'uname':
    print(os.environ.get('FAKE_OS', 'Linux'))
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
    path = target(args[0])
    fd, name = tempfile.mkstemp(prefix='.tailhop.', dir=path.parent)
    os.close(fd)
    print(name)
elif tool == 'mv':
    target(args[-2]).replace(target(args[-1]))
elif tool == 'rm':
    target(args[-1]).unlink(missing_ok=True)
elif tool == 'systemctl':
    if os.environ.get('FAIL_RESTART') and args[0] == 'restart':
        sys.exit(1)
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
        shutil.copytree(ROOT / 'packaging', self.payload / 'packaging')
        (self.payload / 'tailhop').write_bytes(b'new binary')
        self.host = self.root / 'host'
        self.host.mkdir()
        self.accounts = self.root / 'accounts.json'
        self.accounts.write_text('{}')
        tools = self.root / 'tools'
        tools.mkdir()
        for name in ('uname', 'id', 'getent', 'groupadd', 'useradd', 'install',
                     'mktemp', 'mv', 'rm', 'systemctl', 'nologin'):
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
            result = self.run_installer()
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(binary.read_bytes(), b'new binary')
        self.assertEqual(binary.stat().st_mode & 0o777, 0o755)
        self.assertEqual(old_inode.read_bytes(), b'old binary')
        unit = self.host / 'etc/systemd/system/tailhop.service'
        self.assertEqual(unit.read_bytes(), (ROOT / 'packaging/systemd/tailhop.service').read_bytes())
        self.assertEqual(unit.stat().st_mode & 0o777, 0o644)
        for relative in ('etc/tailhop/tailhop.env', 'var/lib/tailhop/tailscaled.state',
                         'etc/systemd/system/tailhop.service.d/custom.conf'):
            path = self.host / relative
            self.assertEqual(path.read_bytes(), b'private existing content')
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(len(self.calls('groupadd')), 1)
        self.assertEqual(len(self.calls('useradd')), 1)
        self.assertFalse(any(call[0] in ('enable', 'restart', 'start', 'stop')
                             for call in self.calls('systemctl')))
        self.assertFalse(list(self.host.rglob('.tailhop.*')))

    def test_start_enables_and_restarts_after_reload(self):
        result = self.run_installer('--start')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls('systemctl')[-3:],
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


if __name__ == '__main__':
    unittest.main()
