#!/usr/bin/env bash
set -euo pipefail

case "${GOOS}/${GOARCH}" in
  linux/amd64)
    archive=dataclient_linux.zip
    binary=gen3-client
    ;;
  darwin/amd64)
    archive=dataclient_osx.zip
    binary=gen3-client
    ;;
  darwin/arm64)
    archive=dataclient_osx_arm64.zip
    binary=gen3-client
    ;;
  windows/amd64)
    archive=dataclient_win64.zip
    binary=gen3-client.exe
    ;;
  *)
    echo "Unsupported target: ${GOOS}/${GOARCH}" >&2
    exit 1
    ;;
esac

build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
go build -o "$build_dir/$binary" .

if [[ "${GITHUB_PULL_REQUEST:-false}" == "false" ]]; then
  zip -jq "$archive" "$build_dir/$binary"
  mkdir -p "$HOME/shared"
  mv "$archive" "$HOME/shared/"
  aws s3 sync "$HOME/shared" "s3://cdis-dc-builds/$GITHUB_BRANCH"
fi
