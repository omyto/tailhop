"""Promote a stable Docker Hub index under the workflow's shared promotion lock."""

import hashlib
import json
import os
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request


STABLE = re.compile(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z')
VERSION = 'org.opencontainers.image.version'
MEDIA_TYPES = (
    'application/vnd.oci.image.index.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
)


def version(value):
    if not isinstance(value, str) or not STABLE.fullmatch(value):
        raise ValueError('Expected a stable version without a leading v')
    return tuple(map(int, value.split('.')))


class Registry:
    def __init__(self, image):
        if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*/tailhop', image):
            raise ValueError('Invalid Docker Hub image name')
        self.image = image
        query = urllib.parse.urlencode({
            'service': 'registry.docker.io', 'scope': f'repository:{image}:pull',
        })
        # The image is public. This read-only bearer is separate from Docker's
        # authenticated push credentials and must never be printed.
        with urllib.request.urlopen('https://auth.docker.io/token?' + query, timeout=30) as response:
            self.token = json.load(response)['token']

    def manifest(self, reference, missing_ok=False):
        request = urllib.request.Request(
            f'https://registry-1.docker.io/v2/{self.image}/manifests/{reference}',
            headers={'Authorization': 'Bearer ' + self.token,
                     'Accept': ', '.join(MEDIA_TYPES)},
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                raw = response.read()
        except urllib.error.HTTPError as error:
            if missing_ok and error.code == 404:
                return None
            raise
        return json.loads(raw), 'sha256:' + hashlib.sha256(raw).hexdigest()


def index_version(manifest):
    if manifest.get('mediaType') not in MEDIA_TYPES:
        raise ValueError('Expected a multi-platform image index')
    platforms = {(item.get('platform', {}).get('os'),
                  item.get('platform', {}).get('architecture'))
                 for item in manifest.get('manifests', [])}
    if not {('linux', 'amd64'), ('linux', 'arm64')} <= platforms:
        raise ValueError('Image index must support Linux amd64 and arm64')
    return version(manifest.get('annotations', {}).get(VERSION))


def promote(image, release, digest, registry, run=subprocess.run):
    candidate = version(release)
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError('Invalid build digest')
    # Resolve the version tag too: both published tags must identify the exact
    # index returned by build-push-action, including both supported platforms.
    manifest, published_digest = registry.manifest(release)
    if published_digest != digest or index_version(manifest) != candidate:
        raise ValueError('Versioned image does not match this build')
    current = registry.manifest('latest', missing_ok=True)
    if current is not None and index_version(current[0]) > candidate:
        print('Keeping latest: a newer stable version is already published')
        return
    # Only an explicit 404 permits first publication. Network/auth/metadata
    # errors fail closed rather than treating an unreadable latest as absent.
    run(['docker', 'buildx', 'imagetools', 'create', '--tag', image + ':latest',
         image + '@' + digest], check=True)
    if registry.manifest('latest')[1] != digest:
        raise ValueError('Latest does not match the versioned image digest')
    print('Verified latest and versioned tags reference the same image index')


if __name__ == '__main__':
    try:
        image = os.environ['DOCKERHUB_USERNAME'] + '/tailhop'
        promote(image, os.environ['RELEASE_VERSION'], os.environ['IMAGE_DIGEST'], Registry(image))
    except Exception:
        # Never dump authenticated requests, responses, or credential-bearing errors.
        raise SystemExit('Latest promotion failed; inspect registry availability and image metadata') from None
