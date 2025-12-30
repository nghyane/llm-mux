#!/usr/bin/env bash
#
# docker-build.sh - Build and run llm-mux Docker container
#

set -euo pipefail

echo "Please select an option:"
echo "1) Run using Pre-built Image (Recommended)"
echo "2) Build from Source and Run (For Developers)"
read -r -p "Enter choice [1-2]: " choice

case "$choice" in
  1)
    echo "--- Running with Pre-built Image ---"
    docker compose up -d --remove-orphans --no-build
    echo "Services are starting from remote image."
    echo "Run 'docker compose logs -f' to see the logs."
    ;;
  2)
    echo "--- Building from Source and Running ---"

    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')"
    COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'none')"
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    echo "Building with:"
    echo "  Version: ${VERSION}"
    echo "  Commit: ${COMMIT}"
    echo "  Build Date: ${BUILD_DATE}"

    export LLM_MUX_IMAGE="llm-mux:local"

    docker compose build \
      --build-arg VERSION="${VERSION}" \
      --build-arg COMMIT="${COMMIT}" \
      --build-arg BUILD_DATE="${BUILD_DATE}"

    docker compose up -d --remove-orphans --pull never

    echo "Build complete. Run 'docker compose logs -f' to see logs."
    ;;
  *)
    echo "Invalid choice. Please enter 1 or 2."
    exit 1
    ;;
esac
