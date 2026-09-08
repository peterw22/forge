#!/usr/bin/env bash
# Explicitly install the driver and full Chromium matching the pinned Go module.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="$(go list -m -f '{{.Version}}' github.com/playwright-community/playwright-go)"
go run "github.com/playwright-community/playwright-go/cmd/playwright@${VERSION}" install chromium
