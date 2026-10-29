#!/bin/sh
# Install an adjacent payload, or download a verified public release when piped to sh.
set -eu

usage() {
    cat <<'EOF'
Usage: sh ./install.sh [--download] [--version vX.Y.Z] [--no-start]
       curl -fsSL <installer-url> | sh -s -- [--version vX.Y.Z] [--no-start]

Install the adjacent tailhop payload on Linux with systemd.
When piped to sh, download the latest public stable release automatically.
--download  Download even when running a saved script (needs curl and standard Linux tools).
--version   Download a specific stable release tag instead of latest.
--no-start  Install only; do not start, stop, or enable the service.
--start     Enable and start/restart explicitly (the default; kept for compatibility).
Local installation needs root; downloads use sudo for installation if needed.
Default: install, enable at boot, and start (or restart) the service.
Existing configuration, systemd drop-ins, and tsnet state are preserved.
EOF
}

fail() { printf 'tailhop installer: %s\n' "$1" >&2; exit 1; }

download_release() (
    start=$1
    version=$2
    for tool in curl awk tar gzip sha256sum sort cmp mkdir mktemp rm; do
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
    case "$version" in
        latest) ;;
        *[!v0-9.]*|'') fail 'Expected a stable tag such as v0.1.0.' ;;
        *) awk -v version="$version" 'BEGIN {
            exit (version !~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/)
        }' || fail 'Expected a stable tag such as v0.1.0.' ;;
    esac
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
    # Parse JSON structure rather than matching text inside descriptions or other
    # assets. Escaped object keys are rejected; GitHub uses plain ASCII keys.
    LC_ALL=C awk -v arch="$arch" -v version="$version" '
    function bad() { exit 1 }
    function space() { while (substr(json, pos, 1) ~ /^[ \t\r\n]$/) pos++ }
    function string(    out, c, escape, hex) {
        if (substr(json, pos++, 1) != "\"") bad()
        while (pos <= length(json)) {
            c = substr(json, pos++, 1)
            if (c == "\"") return out
            if (c ~ /[[:cntrl:]]/) bad()
            if (c == "\\") {
                escape = substr(json, pos++, 1)
                if (escape == "u") {
                    hex = substr(json, pos, 4)
                    if (length(hex) != 4 || hex ~ /[^0-9a-fA-F]/) bad()
                    pos += 4
                    c = c escape hex
                } else {
                    if (escape !~ /^["\\\/bfnrt]$/) bad()
                    c = c escape
                }
            }
            out = out c
        }
        bad()
    }
    function save(path, type, token,    parts, n) {
        if (path == SUBSEP "tag_name") {
            if (type != "string") bad()
            tag = token
        } else if (path == SUBSEP "draft" || path == SUBSEP "prerelease") {
            if (type != "literal" || token != "false") bad()
            flags++
        } else {
            n = split(path, parts, SUBSEP)
            if (n == 4 && parts[2] == "assets") {
                if (parts[4] == "name" || parts[4] == "state" || parts[4] == "digest") {
                    if (type != "string") bad()
                    asset[parts[3], parts[4]] = token
                }
            }
        }
    }
    function value(path, depth,    c, key, token, i) {
        if (depth > 64) bad()
        space()
        c = substr(json, pos, 1)
        if (path == "" && c != "{") bad()
        if (path == SUBSEP "assets" && c != "[") bad()
        if (path ~ /^\034assets\034[0-9]+$/ && c != "{") bad()
        if (c == "{") {
            pos++; space()
            if (substr(json, pos, 1) == "}") { pos++; return }
            do {
                space(); key = string()
                if (index(key, "\\") || seen[path SUBSEP key]++) bad()
                space()
                if (substr(json, pos++, 1) != ":") bad()
                value(path SUBSEP key, depth + 1); space()
                c = substr(json, pos++, 1)
            } while (c == ",")
            if (c != "}") bad()
        } else if (c == "[") {
            pos++; space()
            if (substr(json, pos, 1) == "]") { pos++; return }
            do {
                i++
                value(path SUBSEP i, depth + 1); space()
                c = substr(json, pos++, 1)
            } while (c == ",")
            if (c != "]") bad()
            if (path == SUBSEP "assets") assets = i
        } else if (c == "\"") {
            save(path, "string", string())
        } else {
            token = substr(json, pos)
            if (match(token, /^(true|false|null)/)) {
                token = substr(token, 1, RLENGTH)
                save(path, "literal", token)
            } else if (match(token, /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?/)) {
                token = substr(token, 1, RLENGTH)
                save(path, "number", token)
            } else bad()
            pos += length(token)
        }
    }
    { json = json $0 "\n" }
    END {
        pos = 1
        value("", 0); space()
        if (pos <= length(json) || flags != 2 || tag !~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/) bad()
        if (version != "latest" && tag != version) bad()
        name = "tailhop_" substr(tag, 2) "_linux_" arch ".tar.gz"
        for (i = 1; i <= assets; i++) {
            if (asset[i, "name"] == name) {
                matches++
                if (asset[i, "state"] != "uploaded") bad()
                digest = asset[i, "digest"]
            }
        }
        if (matches != 1 || length(digest) != 71 || digest !~ /^sha256:[0-9a-f]+$/) bad()
        print "https://github.com/omyto/tailhop/releases/download/" tag "/" name
        print substr(digest, 8)
    }' "$download_tmp/release.json" > "$download_tmp/asset" ||
        fail 'Invalid release metadata, unavailable archive, or missing GitHub SHA-256 digest; refusing installation.'
    {
        IFS= read -r asset_url
        IFS= read -r expected_digest
    } < "$download_tmp/asset"
    printf 'Downloading %s\n' "$asset_url"
    curl -q -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 \
        --retry 3 -o "$download_tmp/archive.tar.gz" "$asset_url" || fail 'Archive download failed.'
    actual_digest=$(sha256sum "$download_tmp/archive.tar.gz")
    [ "${actual_digest%% *}" = "$expected_digest" ] || fail 'Archive SHA-256 mismatch; refusing installation.'
    # Check all names and entry types before extracting into a private directory.
    LC_ALL=C tar -tzf "$download_tmp/archive.tar.gz" > "$download_tmp/names" || fail 'Cannot list archive.'
    LC_ALL=C sort "$download_tmp/names" > "$download_tmp/actual"
    LC_ALL=C sort > "$download_tmp/expected" <<'EOF'
tailhop
install.sh
LICENSE
THIRD_PARTY_NOTICES
INSTALL.md
packaging/systemd/tailhop.service
packaging/systemd/tailhop.env.example
EOF
    cmp -s "$download_tmp/actual" "$download_tmp/expected" || fail 'Unexpected archive layout; refusing installation.'
    LC_ALL=C tar -tvzf "$download_tmp/archive.tar.gz" > "$download_tmp/types" || fail 'Cannot inspect archive.'
    awk 'substr($0, 1, 1) != "-" { bad = 1 } END { exit (bad || NR != 7) }' "$download_tmp/types" ||
        fail 'Unexpected archive layout or unsafe entry; refusing installation.'
    mkdir "$download_tmp/payload"
    tar -xzf "$download_tmp/archive.tar.gz" --no-same-owner --no-same-permissions -C "$download_tmp/payload"
    # Pass the choice explicitly to the release-bundled installer.
    if [ "$start" = true ]; then
        set -- --start
    else
        set -- --no-start
    fi
    if [ "$(id -u)" = 0 ]; then
        sh "$download_tmp/payload/install.sh" "$@"
    else
        sudo sh "$download_tmp/payload/install.sh" "$@"
    fi
)

main() {
    start=true
    download=false
    version=latest
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --start) start=true ;;
            --no-start) start=false ;;
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
