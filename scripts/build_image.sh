#!/usr/bin/env sh
set -eu

VERSION=${VERSION:-0.2.0}
IMAGE=${VENATOR_IMAGE:-ghcr.io/0x4d31/venator:v${VERSION}}
VCS_REF=${VCS_REF:-$(git rev-parse --short HEAD 2>/dev/null || printf 'unknown')}
BUILD_DATE=${BUILD_DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}

docker build \
  --build-arg "VERSION=${VERSION}" \
  --build-arg "VCS_REF=${VCS_REF}" \
  --build-arg "BUILD_DATE=${BUILD_DATE}" \
  --tag "${IMAGE}" \
  .

printf 'Built %s\n' "${IMAGE}"

if [ "${PUSH:-false}" = "true" ]; then
  docker push "${IMAGE}"
fi
