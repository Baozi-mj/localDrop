#!/usr/bin/env bash
# tools/dist.sh — build release archives for every supported platform into dist/.
#
# Usage:  ./tools/dist.sh
#
# Produces dist/localdrop_<version>_<os>_<arch>.zip, each containing the binary,
# README.md and LICENSE. dist/ is git-ignored: attach the archives to a GitHub
# release instead of committing them.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)

version=$(sed -n 's/.*appVersion *= *"\([^"]*\)".*/\1/p' util.go)
[ -n "$version" ] || { echo "cannot read appVersion from util.go" >&2; exit 1; }

targets="darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
rm -rf dist
mkdir -p dist

for target in $targets; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  [ "$os" = windows ] && ext=.exe

  stage=$(mktemp -d)
  echo "==> $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w" -o "$stage/localdrop$ext" .
  cp README.md LICENSE "$stage/"

  out="dist/localdrop_${version}_${os}_${arch}.zip"
  (cd "$stage" && zip -q -r "$root/$out" .)
  rm -rf "$stage"
done

echo
ls -lh dist
