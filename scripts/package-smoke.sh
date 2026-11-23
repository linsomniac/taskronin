#!/bin/sh
set -eu
export LC_ALL=C

fail() {
	echo "package smoke: $*" >&2
	exit 1
}

client_line() {
	output=$("$1" version) || fail "$1 version failed"
	line=$(printf '%s\n' "$output" | sed -n '1p')
	case "$line" in
		*dev*) fail "development version: $line" ;;
		"tkr "[0-9]*) printf '%s\n' "$line" ;;
		*) fail "unexpected client version: $line" ;;
	esac
}

if [ "${1:-}" = --as-user ]; then
	export XDG_RUNTIME_DIR="$HOME/run"
	mkdir -m 0700 "$XDG_RUNTIME_DIR"
	cd "$HOME"
	tkr init --no-systemd --password-stdin --timezone America/Denver -y <<'PASSWORD' || fail 'tkr init failed'
package smoke password
PASSWORD
	client=$(client_line tkr)
	version=$(printf '%s\n' "$client" | awk '{print $2}')
	tkr serve &
	server=$!
	trap 'kill -TERM "$server" 2>/dev/null || :; wait "$server" 2>/dev/null || :' EXIT
	trap 'exit 1' HUP INT TERM

	# AIDEV-NOTE: tkr version succeeds even when the server is unreachable.
	# Only a reply with this binary's version proves that startup succeeded.
	deadline=$(( $(date +%s) + 20 ))
	while :; do
		output=$(tkr version) || fail 'tkr version failed during startup'
		if printf '%s\n' "$output" | grep -Fxq "server $version"; then
			break
		fi
		[ "$(date +%s)" -lt "$deadline" ] || fail "server $version unreachable after 20 seconds"
		sleep 1
	done
	tkr add "package smoke" || fail 'tkr add failed'
	tasks=$(tkr ls) || fail 'tkr ls failed'
	printf '%s\n' "$tasks" | grep -Fq 'package smoke' || fail 'task missing from tkr ls'
	kill -TERM "$server" || fail 'could not stop server'
	wait "$server" || fail 'server exited unsuccessfully after SIGTERM'
	trap - EXIT HUP INT TERM
	echo 'package smoke: ok'
	exit 0
fi

# The literal pattern in $1 identifies zero and multiple matches in CI logs.
one_file() {
	pattern=$1
	shift
	[ "$#" -eq 1 ] && [ -f "$1" ] || fail "expected exactly one file matching $pattern"
	printf '%s\n' "$1"
}

check_file() {
	[ -f "$1" ] || fail "missing $1"
	info=$(stat -c '%a %U:%G' "$1")
	[ "$info" = "$2 root:root" ] || fail "$1 has $info, expected $2 root:root"
}

case "$0" in
	/*) ;;
	*) exec sh "$PWD/$0" "$@" ;;
esac
cd "${1:-/dist}" || fail 'cannot open dist directory'
arch=$(dpkg --print-architecture)
deb=$(one_file "taskronin_*_${arch}.deb" taskronin_*_"$arch".deb)
binary=$(one_file "taskronin_*_linux_${arch}.tar.gz" taskronin_*_linux_"$arch".tar.gz)
source=$(one_file 'taskronin_*_source.tar.gz' taskronin_*_source.tar.gz)
sha256sum -c checksums.txt || fail 'checksum verification failed'
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "./$deb" || fail "could not install $deb"

package=$(dpkg-query -W -f='${Package}|${Depends}' taskronin) || fail 'taskronin is missing'
[ "$package" = 'taskronin|' ] || fail "unexpected package or Depends: $package"
check_file /usr/bin/tkr 755
check_file /usr/share/bash-completion/completions/tkr 644
check_file /usr/share/zsh/vendor-completions/_tkr 644
check_file /usr/share/fish/vendor_completions.d/tkr.fish 644
for name in copyright THIRD_PARTY_LICENSES.txt README.md quickstart.md; do
	check_file "/usr/share/doc/taskronin/$name" 644
done
client=$(client_line /usr/bin/tkr)
version=$(printf '%s\n' "$client" | awk '{print $2}')

scratch=$(mktemp -d "$HOME/package-smoke.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
tar -tzf "$binary" >"$scratch/entries"
sort "$scratch/entries" >"$scratch/sorted"
diff -u - "$scratch/sorted" <<'ENTRIES' || fail 'unexpected binary tarball contents'
LICENSE
README.md
THIRD_PARTY_LICENSES.txt
completions/_tkr
completions/tkr.bash
completions/tkr.fish
quickstart.md
tkr
ENTRIES
tar -xzf "$binary" -C "$scratch" tkr
archived=$(client_line "$scratch/tkr")
[ "$archived" = "$client" ] || fail "tarball version differs from installed version: $archived"

tar -tzf "$source" >"$scratch/entries"
prefix="taskronin-$version/"
awk -v prefix="$prefix" 'index($0, prefix) != 1 { exit 1 }' "$scratch/entries" || fail "unexpected source directory (expected $prefix)"
for name in go.mod LICENSE cmd/tkr/main.go; do
	grep -Fxq "$prefix$name" "$scratch/entries" || fail "source tarball lacks $prefix$name"
done

useradd -m smoke
setpriv --reuid=smoke --regid=smoke --init-groups env -i HOME=/home/smoke PATH=/usr/bin:/bin sh "$0" --as-user
