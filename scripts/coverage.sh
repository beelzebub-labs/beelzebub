#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${COVERAGE_IMAGE:-golang:1.25.9-alpine}"

docker run --rm \
  -v "${ROOT_DIR}:/src" \
  -w /src \
  "${IMAGE}" \
  sh /src/scripts/coverage-container.sh
