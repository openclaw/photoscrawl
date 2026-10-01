#!/usr/bin/env bash
set -euo pipefail

if (( $# == 0 )); then
  echo "usage: check_macos_minimum.sh BINARY..." >&2
  exit 1
fi

for binary in "$@"; do
  if ! otool -l "$binary" | awk '
    $1 == "minos" {
      found = 1
      if ($2 != "13.0") failed = 1
    }
    END { exit (!found || failed) }
  '; then
    echo "$binary must target macOS 13.0 in every architecture" >&2
    exit 1
  fi
done
