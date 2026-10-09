#!/usr/bin/env bash
# Print the resolved Playwright version from a pnpm lockfile (default:
# web/pnpm-lock.yaml). The CI workflow keys the ms-playwright browser cache on
# this, so bumping Playwright invalidates the cache and a fresh Chromium is
# downloaded exactly once per version.
set -euo pipefail

lockfile="${1:-web/pnpm-lock.yaml}"

version="$(awk '
  /^  playwright@[0-9]/ {
    sub(/^  playwright@/, "")
    sub(/:.*/, "")
    print
    exit
  }
' "$lockfile")"

if [ -z "$version" ]; then
  echo "playwright-version: no playwright entry in $lockfile" >&2
  exit 1
fi
printf '%s\n' "$version"
