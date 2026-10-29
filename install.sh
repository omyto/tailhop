#!/bin/sh
# Install a local, verified payload without downloads or running the binary in preflight.
set -eu

usage() {
    cat <<'EOF'
Usage: sudo sh ./install.sh [--start]

Install the adjacent tailhop binary and packaged systemd unit on Linux.
--start  Enable at boot and start (or restart) the service after installation.
Default: install only; do not start, stop, or enable the service.
Existing configuration, systemd drop-ins, and tsnet state are preserved.
EOF
}

fail() { printf 'tailhop installer: %s\n' "$1" >&2; exit 1; }

start=false
for arg in "$@"; do
    case "$arg" in
        --start) start=true ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; fail 'Unknown argument.' ;;
    esac
done

[ "$(uname -s)" = Linux ] || fail 'Linux with systemd is required.'
[ "$(id -u)" = 0 ] || fail 'Run this script as root (for example with sudo).'
for tool in getent groupadd useradd install mktemp mv rm systemctl; do
    command -v "$tool" >/dev/null 2>&1 || fail "Required command is missing: $tool"
done
systemctl --system show --property=Version --value >/dev/null ||
    fail 'A running systemd system manager is required.'

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
for file in tailhop packaging/systemd/tailhop.service; do
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
