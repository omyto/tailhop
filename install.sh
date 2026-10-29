#!/bin/sh
# Install an adjacent payload, or download a verified public release when piped to sh.
set -eu

usage() {
    cat <<'EOF'
Usage: sh ./install.sh [--download] [--version vX.Y.Z] [--start]
       curl -fsSL <installer-url> | sh -s -- [--version vX.Y.Z] [--start]

Install the adjacent tailhop payload on Linux with systemd.
When piped to sh, download the latest public stable release automatically.
--download  Download even when running a saved script (needs curl and Python 3).
--version   Download a specific stable release tag instead of latest.
--start     Enable at boot and start (or restart) after installation.
Local installation needs root; downloads use sudo for installation if needed.
Default: install only; do not start, stop, or enable the service.
Existing configuration, systemd drop-ins, and tsnet state are preserved.
EOF
}

fail() { printf 'tailhop installer: %s\n' "$1" >&2; exit 1; }

download_release() (
    start=$1
    version=$2
    for tool in curl python3 mktemp rm; do
        command -v "$tool" >/dev/null 2>&1 || fail "Required download command is missing: $tool"
    done
    if [ "$(id -u)" != 0 ]; then
        command -v sudo >/dev/null 2>&1 || fail 'Installation needs root; install sudo or run as root.'
    fi
    case "$(uname -m)" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) fail 'Only Linux amd64 and arm64 releases are available.' ;;
    esac
    python3 - "$version" <<'PY'
import re, sys
if sys.argv[1] != 'latest' and not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', sys.argv[1]):
    sys.exit('tailhop installer: Expected a stable tag such as v0.1.0.')
PY
    umask 077
    download_tmp=$(mktemp -d)
    trap 'rm -rf -- "$download_tmp"' EXIT
    trap 'exit 1' HUP INT TERM
    api=https://api.github.com/repos/omyto/tailhop/releases
    if [ "$version" = latest ]; then
        api=$api/latest
    else
        api=$api/tags/$version
    fi
    curl -q -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 \
        --retry 3 -H 'Accept: application/vnd.github+json' -o "$download_tmp/release.json" "$api" ||
        fail 'Cannot read a public release; check connectivity, GitHub rate limits, and release availability.'
    python3 - "$download_tmp/release.json" "$arch" "$version" > "$download_tmp/asset" <<'PY'
import json, re, sys
with open(sys.argv[1]) as source:
    release = json.load(source)
tag = release.get('tag_name', '')
if (release.get('draft') is not False or release.get('prerelease') is not False
        or not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', tag)
        or (sys.argv[3] != 'latest' and tag != sys.argv[3])):
    sys.exit('tailhop installer: Expected a published stable release.')
name = f'tailhop_{tag[1:]}_linux_{sys.argv[2]}.tar.gz'
assets = [asset for asset in release.get('assets', []) if asset.get('name') == name]
if len(assets) != 1 or assets[0].get('state') != 'uploaded':
    sys.exit('tailhop installer: Release has no unique uploaded archive for this architecture.')
digest = assets[0].get('digest') or ''
if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
    sys.exit('tailhop installer: Missing GitHub asset SHA-256 digest; refusing installation.')
print(f'https://github.com/omyto/tailhop/releases/download/{tag}/{name}')
print(digest[7:])
PY
    {
        IFS= read -r asset_url
        IFS= read -r expected_digest
    } < "$download_tmp/asset"
    printf 'Downloading %s\n' "$asset_url"
    curl -q -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 \
        --retry 3 -o "$download_tmp/archive.tar.gz" "$asset_url" || fail 'Archive download failed.'
    python3 - "$download_tmp" "$expected_digest" <<'PY'
import hashlib, pathlib, shutil, sys, tarfile
work = pathlib.Path(sys.argv[1])
archive = work / 'archive.tar.gz'
digest = hashlib.sha256()
with archive.open('rb') as source:
    for chunk in iter(lambda: source.read(1024 * 1024), b''):
        digest.update(chunk)
if digest.hexdigest() != sys.argv[2]:
    sys.exit('tailhop installer: Archive SHA-256 mismatch; refusing installation.')
required = {'tailhop', 'install.sh', 'LICENSE', 'THIRD_PARTY_NOTICES', 'INSTALL.md',
            'packaging/systemd/tailhop.service', 'packaging/systemd/tailhop.env.example'}
with tarfile.open(archive, 'r:gz') as payload:
    members = payload.getmembers()
    if (len(members) != len(required) or {m.name for m in members} != required
            or any(not m.isfile() for m in members)):
        sys.exit('tailhop installer: Unexpected archive layout or unsafe entry; refusing installation.')
    # Copy only known regular files into a private directory; never extract links.
    for member in members:
        target = work / 'payload' / member.name
        target.parent.mkdir(parents=True, exist_ok=True)
        with payload.extractfile(member) as source, target.open('wb') as destination:
            shutil.copyfileobj(source, destination)
        target.chmod(0o755 if member.name in ('tailhop', 'install.sh') else 0o644)
PY
    set --
    [ "$start" = false ] || set -- --start
    if [ "$(id -u)" = 0 ]; then
        sh "$download_tmp/payload/install.sh" "$@"
    else
        sudo sh "$download_tmp/payload/install.sh" "$@"
    fi
)

main() {
    start=false
    download=false
    version=latest
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --start) start=true ;;
            --download) download=true ;;
            --version)
                [ "$#" -ge 2 ] || fail '--version requires a stable tag such as v0.1.0.'
                version=$2
                download=true
                shift
                ;;
            -h|--help) usage; exit 0 ;;
            *) usage >&2; fail 'Unknown argument.' ;;
        esac
        shift
    done

    [ "$(uname -s)" = Linux ] || fail 'Linux with systemd is required.'
    # With stdin scripts, $0 names the shell, possibly by its absolute path.
    case "${0##*/}" in
        sh|dash|bash|ash|-sh|-dash|-bash|-ash) download=true ;;
    esac
    if [ "$download" = true ] || [ ! -f "$0" ]; then
        download_release "$start" "$version"
        return
    fi
    [ "$(id -u)" = 0 ] || fail 'Run this script as root (for example with sudo).'
    for tool in getent groupadd useradd install mktemp mv rm systemctl; do
        command -v "$tool" >/dev/null 2>&1 || fail "Required command is missing: $tool"
    done
    systemctl --system show --property=Version --value >/dev/null ||
        fail 'A running systemd system manager is required.'

    source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
    for file in tailhop packaging/systemd/tailhop.service LICENSE THIRD_PARTY_NOTICES; do
        [ -f "$source_dir/$file" ] && [ ! -L "$source_dir/$file" ] ||
            fail "Missing regular payload file: $file (extract a verified archive or build tailhop first)."
    done

    # Refuse to repurpose a login account or a group listing other members.
    group_entry=$(getent group tailhop) || group_entry=
    user_entry=$(getent passwd tailhop) || user_entry=
    if [ -n "$group_entry" ]; then
        IFS=: read -r group_name group_password group_id group_members <<EOF
$group_entry
EOF
        [ "$group_id" != 0 ] || fail 'The tailhop group must not be root.'
        case "$group_members" in
            ''|tailhop) ;;
            *) fail 'The tailhop group has other members; review it manually.' ;;
        esac
    fi
    if [ -n "$user_entry" ]; then
        IFS=: read -r user_name user_password user_id user_group user_info user_home user_shell <<EOF
$user_entry
EOF
        [ -n "$group_entry" ] && [ "$user_id" != 0 ] &&
            [ "$user_group" = "$group_id" ] && [ "$user_home" = /var/lib/tailhop ] ||
            fail 'Existing tailhop account is not the expected dedicated service account.'
        case "$user_shell" in
            */nologin|/bin/false|/usr/bin/false) ;;
            *) fail 'Existing tailhop account permits login; review it manually.' ;;
        esac
    else
        nologin=$(command -v nologin) || fail 'Required command is missing: nologin'
    fi

    # Stage complete files on the destination filesystems before replacing either.
    # Rename also works when the old executable is still running.
    binary_tmp=
    unit_tmp=
    cleanup() {
        [ -z "$binary_tmp" ] || rm -f -- "$binary_tmp"
        [ -z "$unit_tmp" ] || rm -f -- "$unit_tmp"
        :
    }
    trap cleanup EXIT
    trap 'exit 1' HUP INT TERM
    umask 077
    install -d -o root -g root -m 0755 /usr/local/bin /etc/systemd/system
    binary_tmp=$(mktemp /usr/local/bin/.tailhop.XXXXXX)
    unit_tmp=$(mktemp /etc/systemd/system/.tailhop.XXXXXX)
    install -o root -g root -m 0755 "$source_dir/tailhop" "$binary_tmp"
    install -o root -g root -m 0644 "$source_dir/packaging/systemd/tailhop.service" "$unit_tmp"
    install -d -o root -g root -m 0755 /usr/local/share/doc/tailhop
    install -o root -g root -m 0644 "$source_dir/LICENSE" /usr/local/share/doc/tailhop/LICENSE
    install -o root -g root -m 0644 "$source_dir/THIRD_PARTY_NOTICES" /usr/local/share/doc/tailhop/THIRD_PARTY_NOTICES

    [ -n "$group_entry" ] || groupadd --system tailhop
    [ -n "$user_entry" ] || useradd --system --gid tailhop --home-dir /var/lib/tailhop \
        --no-create-home --shell "$nologin" tailhop
    mv -fT -- "$binary_tmp" /usr/local/bin/tailhop
    binary_tmp=
    mv -fT -- "$unit_tmp" /etc/systemd/system/tailhop.service
    unit_tmp=
    systemctl daemon-reload

    if [ "$start" = true ]; then
        systemctl enable tailhop
        systemctl restart tailhop
        printf '%s\n' 'Service started. Follow enrollment logs: sudo journalctl -u tailhop -f'
    else
        printf '%s\n' 'Installed. To enable and start: sudo systemctl enable --now tailhop' \
            'If already running, restart to use the new binary: sudo systemctl restart tailhop'
    fi
    printf '%s\n' 'Configuration and state were preserved; state is not migrated.'
}

# Parse the complete script before executing a streamed download.
main "$@"
