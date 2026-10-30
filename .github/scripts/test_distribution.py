"""Exercise the actual archive recipe with a fixture binary, without Go or a registry."""

import os
from pathlib import Path
import re
import shlex
import subprocess
import tarfile
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[2]
MATERIALS = ['install.sh', 'LICENSE', 'THIRD_PARTY_NOTICES', 'INSTALL.md',
             'packaging/systemd/tailhop.service', 'packaging/systemd/tailhop.env.example']


class DistributionTests(unittest.TestCase):
    def test_required_materials_and_runtime_copy(self):
        for name in MATERIALS:
            path = ROOT / name
            self.assertTrue(path.is_file() and not path.is_symlink(), name)
            self.assertGreater(path.stat().st_size, 0, name)
        dockerfile = (ROOT / 'Dockerfile').read_text()
        runtime = dockerfile.split('FROM debian:', 1)[1]
        self.assertIn('COPY LICENSE THIRD_PARTY_NOTICES /usr/share/doc/tailhop/', runtime)
        self.assertIn('USER 10001:10001', runtime)
        context = (ROOT / '.dockerignore').read_text().splitlines()
        self.assertIn('!LICENSE', context)
        self.assertIn('!THIRD_PARTY_NOTICES', context)

    def test_archive_recipe(self):
        workflow = (ROOT / '.github/workflows/linux-binaries.yml').read_text()
        files = shlex.split(re.search(r'^          files=\((.*)\)$', workflow, re.M)[1])
        self.assertEqual(files, ['tailhop', *MATERIALS])
        # Run the production packaging/integrity recipe. Only its preceding
        # clean-check guard is omitted because contributors test uncommitted edits.
        recipe = workflow.split('          git diff --exit-code HEAD -- ', 1)[1].split('\n', 1)[1]
        recipe = recipe.split('      - name: Upload private build artifact', 1)[0]
        recipe = textwrap.dedent(recipe)
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            tools = work / 'bin'
            tools.mkdir()
            go = tools / 'go'
            go.write_text('''#!/usr/bin/env python3
import os, pathlib, shlex, sys
if sys.argv[1] == 'build':
    flags = shlex.split(next(arg.split('=', 1)[1] for arg in sys.argv if arg.startswith('-ldflags=')))
    definitions = [flags[i + 1] for i, flag in enumerate(flags) if flag == '-X']
    assert definitions == ['main.version=v' + os.environ['VERSION'], 'main.commit=' + os.environ['SOURCE_COMMIT']]
    pathlib.Path(sys.argv[sys.argv.index('-o') + 1]).write_text('fixture-' + os.environ['GOARCH'])
elif sys.argv[1:3] == ['version', '-m']:
    arch = pathlib.Path(sys.argv[3]).read_text().removeprefix('fixture-')
    print('build GOOS=linux\\nbuild GOARCH=' + arch + '\\nbuild CGO_ENABLED=0')
else:
    sys.exit('Unexpected fixture go command')
''')
            go.chmod(0o755)
            env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'],
                       RUNNER_TEMP=tmp, VERSION='0.1.0', SOURCE_COMMIT='a' * 40,
                       GITHUB_OUTPUT=str(work / 'outputs'))
            subprocess.run(['bash', '-euo', 'pipefail', '-c', recipe], cwd=ROOT,
                           env=env, check=True, capture_output=True, text=True)
            for arch in ('amd64', 'arm64'):
                archive = work / 'tailhop-linux-binaries' / f'tailhop_0.1.0_linux_{arch}.tar.gz'
                with tarfile.open(archive) as tar:
                    self.assertEqual(tar.getnames(), files)
                    for member in tar.getmembers():
                        self.assertTrue(member.isfile())
                        self.assertEqual((member.uid, member.gid), (0, 0))
                        self.assertEqual(member.mode, 0o755 if member.name in ('tailhop', 'install.sh') else 0o644)
                        if member.name != 'tailhop':
                            self.assertEqual(tar.extractfile(member).read(), (ROOT / member.name).read_bytes())


if __name__ == '__main__':
    unittest.main()
