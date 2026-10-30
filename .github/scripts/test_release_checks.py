"""Check release dependencies and execute the actual CI security commands offline."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[2]
CI = (ROOT / '.github/workflows/ci.yml').read_text()
TARGETS = ['FuzzAuthority', 'FuzzPublicIP', 'FuzzStripHeaders']


def job(workflow, name):
    jobs = workflow.split('\njobs:\n', 1)[1]
    return re.search(r'^  ' + name + r':\n(.*?)(?=^  \S|\Z)', jobs, re.M | re.S)[1]


def command(step_name):
    step = CI.split('      - name: ' + step_name + '\n', 1)[1].split('\n      - ', 1)[0]
    run = re.search(r'^        run: (.*)$', step, re.M)[1]
    if run == '|':
        return textwrap.dedent(step.split('        run: |\n', 1)[1])
    return run[1:-1] if run.startswith("'") and run.endswith("'") else run


class ReleaseChecksTests(unittest.TestCase):
    def test_publication_requires_security_on_the_payload_commit(self):
        for filename, producer, publication in [
            ('linux-binaries.yml', 'build', 'release'),
            ('docker-publish.yml', 'verify', 'publish'),
        ]:
            with self.subTest(workflow=filename):
                workflow = (ROOT / '.github/workflows' / filename).read_text()
                gate = job(workflow, 'security')
                publish = job(workflow, publication)
                needs = re.search(r'^    needs: (.*)$', publish, re.M)[1]
                self.assertEqual(set(needs.strip('[]').replace(' ', '').split(',')), {producer, 'security'})
                self.assertIn('needs: ' + producer, gate)
                self.assertIn("if: needs." + producer + ".outputs.commit != ''", gate)
                self.assertIn('uses: ./.github/workflows/ci.yml', gate)
                self.assertIn('source_commit: ${{ needs.' + producer + '.outputs.commit }}', gate)
                self.assertNotIn('secrets:', gate)
                self.assertNotIn('continue-on-error', gate + publish)
                # Preserve GitHub's default success condition, which also blocks
                # downstream jobs when a prerequisite is skipped or canceled.
                self.assertIsNone(re.search(r'^    if:', publish, re.M))

    def test_called_ci_checks_out_the_requested_commit_in_every_job(self):
        self.assertIn('  workflow_call:\n    inputs:\n      source_commit:', CI)
        self.assertIn('contents: read', CI)
        self.assertNotIn('contents: write', CI)
        for name in ['test', 'vulnerabilities']:
            self.assertIn('ref: ${{ inputs.source_commit || github.sha }}', job(CI, name))
            self.assertNotIn('continue-on-error', job(CI, name))
        self.assertIn('arch: [amd64, arm64]', job(CI, 'vulnerabilities'))

    def run_fuzz_command(self, **overrides):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            go = directory / 'go'
            go.write_text('''#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
assert args[:2] == ['test', './internal/proxy'], args
if '-list' in args:
    target = args[args.index('-list') + 1].strip('^$')
    mode = 'list'
else:
    target = next(arg.split('=', 1)[1].strip('^$') for arg in args if arg.startswith('-fuzz='))
    mode = 'fuzz'
with open(os.environ['RELEASE_CHECK_CALLS'], 'a') as calls:
    calls.write(mode + ':' + target + '\\n')
if mode == 'list':
    if target == os.environ.get('FAILED_LIST_TARGET'):
        sys.exit(2)
    if target != os.environ.get('MISSING_FUZZ_TARGET'):
        print(target)
elif target == os.environ.get('FAILED_FUZZ_TARGET'):
    sys.exit(1)
''')
            go.chmod(0o755)
            calls = directory / 'calls'
            env = dict(os.environ, PATH=tmp + os.pathsep + os.environ['PATH'],
                       RELEASE_CHECK_CALLS=str(calls), FAILED_LIST_TARGET='',
                       MISSING_FUZZ_TARGET='', FAILED_FUZZ_TARGET='')
            env.update(overrides)
            result = subprocess.run(['bash', '--noprofile', '--norc', '-e', '-o', 'pipefail',
                                     '-c', command('Fuzz policy and proxy headers')],
                                    env=env, capture_output=True, text=True)
            return result, calls.read_text().splitlines()

    def test_all_fuzz_targets_run(self):
        result, calls = self.run_fuzz_command()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [entry for target in TARGETS for entry in ('list:' + target, 'fuzz:' + target)])

    def test_missing_unloadable_or_failed_fuzz_target_stops_the_gate(self):
        for variable, mode in [('MISSING_FUZZ_TARGET', 'list'), ('FAILED_LIST_TARGET', 'list'),
                               ('FAILED_FUZZ_TARGET', 'fuzz')]:
            for target in TARGETS:
                with self.subTest(failure=variable, target=target):
                    result, calls = self.run_fuzz_command(**{variable: target})
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(calls[-1], mode + ':' + target)
                    if mode == 'list':
                        self.assertNotIn('fuzz:' + target, calls)

    def test_scanner_findings_and_errors_fail_the_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            tools = Path(tmp) / 'tailhop-tools'
            tools.mkdir()
            scanner = tools / 'govulncheck'
            scanner.write_text('#!/bin/sh\n[ "$#" = 1 ] && [ "$1" = ./... ] || exit 64\nexit "$SCANNER_EXIT"\n')
            scanner.chmod(0o755)
            for status in [0, 1, 3]:
                with self.subTest(exit_status=status):
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', command('Scan release build configuration')],
                                            env=dict(os.environ, RUNNER_TEMP=tmp, SCANNER_EXIT=str(status)),
                                            capture_output=True, text=True)
                    self.assertEqual(result.returncode, status, result.stderr)


if __name__ == '__main__':
    unittest.main()
