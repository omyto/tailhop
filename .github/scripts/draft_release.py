"""Attach verified binaries to a draft; never delete old assets before uploading replacements."""

import hashlib
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen
import uuid


class API:
    def __init__(self):
        self.root = f"/repos/{os.environ['GITHUB_REPOSITORY']}"
        self.base = os.environ['GITHUB_API_URL']
        self.token = os.environ['GITHUB_TOKEN']

    def request(self, method, path, data=None, content=None):
        url = path if path.startswith('https://') else self.base + path
        headers = {'Authorization': f'Bearer {self.token}',
                   'Accept': 'application/vnd.github+json',
                   'X-GitHub-Api-Version': '2026-03-10'}
        if data is not None:
            content = json.dumps(data).encode()
            headers['Content-Type'] = 'application/json'
        elif content is not None:
            headers['Content-Type'] = 'application/octet-stream'
        try:
            with urlopen(Request(url, data=content, headers=headers, method=method),
                         timeout=120) as response:
                body = response.read()
                return json.loads(body) if body else None
        except HTTPError as error:
            # Do not dump request headers, credentials, or uploaded file content.
            raise RuntimeError(f'GitHub API {method} failed: HTTP {error.code}') from None

    def list(self, path):
        result = []
        page = 1
        while True:
            items = self.request('GET', f'{path}?per_page=100&page={page}')
            result.extend(items)
            if len(items) < 100:
                return result
            page += 1


def digest(content):
    return 'sha256:' + hashlib.sha256(content).hexdigest()


def update(api, tag, version, commit, directory, expected_digests):
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', tag) or tag != 'v' + version:
        raise RuntimeError('Invalid release version')
    names = [f'tailhop_{version}_linux_{arch}.tar.gz' for arch in ('amd64', 'arm64')]
    contents = {name: (directory / name).read_bytes() for name in names}
    if set(expected_digests) != set(names) or not all(contents.values()):
        raise RuntimeError('Missing archive integrity metadata or empty archive')
    if any(not re.fullmatch(r'sha256:[0-9a-f]{64}', expected_digests[name])
           or digest(contents[name]) != expected_digests[name] for name in names):
        raise RuntimeError('Archive does not match the verified build digest')

    # Recheck the existing remote tag against the commit actually built.
    obj = api.request('GET', f'{api.root}/git/ref/tags/{tag}')['object']
    for _ in range(10):
        if obj['type'] != 'tag':
            break
        obj = api.request('GET', f"{api.root}/git/tags/{obj['sha']}")['object']
    if obj['type'] != 'commit' or obj['sha'] != commit:
        raise RuntimeError('Remote release tag no longer matches the built commit')

    matches = [r for r in api.list(f'{api.root}/releases') if r['tag_name'] == tag]
    if any(not r['draft'] for r in matches):
        raise RuntimeError('Refusing to modify a published release')
    if len(matches) > 1:
        raise RuntimeError('Multiple drafts exist for this tag; resolve them manually')
    release = matches[0] if matches else api.request('POST', f'{api.root}/releases', {
        'tag_name': tag, 'target_commitish': commit, 'name': tag,
        'draft': True, 'prerelease': False,
    })
    release_path = f"{api.root}/releases/{release['id']}"

    def draft():
        current = api.request('GET', release_path)
        if current['draft'] is not True or current['tag_name'] != tag or current.get('immutable'):
            raise RuntimeError('Release is no longer a mutable draft; stopping')
        return current

    def assets():
        return {a['name']: a for a in api.list(release_path + '/assets')}

    def same(asset, name):
        return (asset and asset['state'] == 'uploaded'
                and asset['size'] == len(contents[name])
                and asset.get('digest') == digest(contents[name]))

    def rename(asset_id, name):
        draft()
        api.request('PATCH', f'{api.root}/releases/assets/{asset_id}', {'name': name})

    current = assets()
    # Recognize only a legacy manifest whose digest matches the exact old
    # archive hashes/names. Backups allow recovery if a previous cleanup failed.
    legacy = current.get('checksums.txt')
    known_legacy = False
    prefixes = {''}
    for name in current:
        match = re.match(r'(tailhop-backup-[0-9a-f]{32}-)tailhop_', name)
        if match:
            prefixes.add(match[1])
    for prefix in prefixes:
        old = [current.get(prefix + name, current.get(name)) for name in names]
        if all(a and re.fullmatch(r'sha256:[0-9a-f]{64}', a.get('digest') or '') for a in old):
            previous = ''.join(f"{a['digest'][7:]}  {name}\n" for name, a in zip(names, old)).encode()
            if legacy and legacy['state'] == 'uploaded' and legacy['size'] == len(previous) and legacy.get('digest') == digest(previous):
                known_legacy = True
                break
    pending = []
    batch = uuid.uuid4().hex
    # Stage and verify ALL changed uploads before touching canonical names.
    for name, content in contents.items():
        if same(current.get(name), name):
            continue
        live = draft()
        upload_url = live['upload_url'].split('{', 1)[0]
        staging = f'tailhop-staging-{batch}-{name}'
        print(f'Uploading replacement for {name}', flush=True)
        new = api.request('POST', upload_url + '?' + urlencode({'name': staging}), content=content)
        if not same(new, name):
            raise RuntimeError(f'Upload verification failed for {name}; existing assets retained')
        pending.append((name, new, current.get(name)))

    undo = []
    try:
        for name, new, old in pending:
            if old:
                rename(old['id'], f'tailhop-backup-{batch}-{name}')
                undo.append((old['id'], name))
            rename(new['id'], name)
            undo.append((new['id'], new['name']))
        draft()
        final = assets()
        if not all(same(final.get(name), name) for name in contents):
            raise RuntimeError('Final release asset verification failed')
    except Exception:
        # Best effort only: an interrupted/uncertain API call can leave backup names.
        # Keep every asset's bytes; reruns can finish recovery. Never unpublish.
        for asset_id, original_name in reversed(undo):
            try:
                rename(asset_id, original_name)
            except Exception:
                print('Rollback incomplete; retained staging/backup assets need review', file=sys.stderr)
        raise

    # Only after the complete new set is verified may old backups be deleted.
    if known_legacy:
        rename(legacy['id'], f'tailhop-backup-{batch}-checksums.txt')
        final = assets()
    elif legacy:
        print('Retaining unrecognized checksums.txt attachment; review it manually')
    managed = re.compile(r'tailhop-(staging|backup)-[0-9a-f]{32}-(' +
                         '|'.join(re.escape(name) for name in [*contents, 'checksums.txt']) + r')')
    for name, asset in final.items():
        if managed.fullmatch(name):
            draft()
            api.request('DELETE', f"{api.root}/releases/assets/{asset['id']}")
    draft()
    print(f'Verified draft assets ready for {tag}; release remains unpublished')


if __name__ == '__main__':
    try:
        update(API(), os.environ['RELEASE_TAG'], os.environ['VERSION'],
               os.environ['SOURCE_COMMIT'], Path(os.environ['ASSET_DIR']),
               {f"tailhop_{os.environ['VERSION']}_linux_{arch}.tar.gz":
                'sha256:' + os.environ[f'{arch.upper()}_SHA256']
                for arch in ('amd64', 'arm64')})
    except Exception as error:
        print(f'::error::Draft release update failed: {error}', file=sys.stderr)
        sys.exit(1)
