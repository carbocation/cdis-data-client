#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "A macOS host is required to verify the Apple Silicon binary." >&2
  exit 1
fi

output_dir="${1:-dist}"
mkdir -p "$output_dir"
GOOS=darwin GOARCH=arm64 go build -o "$output_dir/gen3-client" .

if [[ "$(uname -m)" == "arm64" ]]; then
  "$output_dir/gen3-client" help
fi

echo "Built $output_dir/gen3-client for arm64"
