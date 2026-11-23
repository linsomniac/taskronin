#!/bin/sh
set -eu
export LC_ALL=C

out=build
mkdir -p "$out/completions"
# Remove stale outputs, including a previous notice if module discovery fails.
rm -f "$out/THIRD_PARTY_LICENSES.txt" "$out"/completions/*
trap 'rm -f "$out/tkr" "$out/modules.sorted" "$out/notices.tmp"' EXIT

# AIDEV-NOTE: enumerate the static release builds separately. A failed go list
# must stop packaging before a partial set of licenses can be published.
template='{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}'
for arch in amd64 arm64; do
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go list -deps -f "$template" ./cmd/tkr >"$out/modules.$arch"
done
LC_ALL=C sort -u "$out/modules.amd64" "$out/modules.arm64" >"$out/modules.sorted"

go build -o "$out/tkr" ./cmd/tkr
"$out/tkr" completion bash >"$out/completions/tkr.bash"
"$out/tkr" completion zsh >"$out/completions/_tkr"
"$out/tkr" completion fish >"$out/completions/tkr.fish"

notice() {
	printf '\n%s\n\n' "$1"
	cat "$2"
}

{
	printf '%s\n' 'Taskronin is dedicated to the public domain under CC0-1.0. Its binaries' \
		'also contain the following software, under the licenses below.'
	# Nix's Go omits GOROOT/LICENSE.
	notice 'Go standard library' packaging/go-LICENSE
	while read -r module dir; do
		found=
		for file in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
			[ -f "$file" ] || continue
			notice "$module (${file##*/})" "$file"
			found=1
		done
		if [ -z "$found" ]; then
			file="packaging/licenses/${module%@*}/LICENSE"
			if [ ! -f "$file" ]; then
				echo "release-assets: no license file for $module in $dir" >&2
				exit 1
			fi
			notice "$module (LICENSE)" "$file"
		fi
	done <"$out/modules.sorted"
	# Source: https://raw.githubusercontent.com/bigskysoftware/htmx/v2.0.11/LICENSE
	notice 'htmx 2.0.11 (embedded web asset)' packaging/licenses/htmx/LICENSE
} >"$out/notices.tmp"

{
	cat <<'HEADER'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: taskronin
Upstream-Contact: Sean Reifschneider <sean@realgo.com>
Source: https://github.com/linsomniac/taskronin

Files: *
Copyright: Sean Reifschneider
License: CC0-1.0
Comment: For the licenses of the Go standard library, linked modules and
 embedded htmx asset, see /usr/share/doc/taskronin/THIRD_PARTY_LICENSES.txt.

License: CC0-1.0
HEADER
	sed -e 's/^$/./' -e 's/^/ /' LICENSE
} >"$out/copyright"
mv "$out/notices.tmp" "$out/THIRD_PARTY_LICENSES.txt"
