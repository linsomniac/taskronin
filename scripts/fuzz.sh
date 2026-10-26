#!/bin/sh
# Runs every Fuzz* target in internal/ for $1 (default 30s) each.
set -eu
t="${1:-30s}"
for d in $(grep -rl --include='*_test.go' '^func Fuzz' internal | xargs -n1 dirname | sort -u); do
  for f in $(grep -h '^func Fuzz' "$d"/*_test.go | sed 's/^func \(Fuzz[A-Za-z0-9_]*\).*/\1/'); do
    go test "./$d" -run='^$' -fuzz="^$f\$" -fuzztime="$t"
  done
done
