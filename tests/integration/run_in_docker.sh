#!/bin/bash

# Run the integration tests in a privileged Linux container, which provides
# what the WireGuard and multi-line tests need (root, network namespaces,
# /dev/net/tun, tc netem) on any host, e.g. macOS:
#
#   ./run_in_docker.sh                                   # full suite
#   ./run_in_docker.sh -tests tcp_forward,tcp_forward_multiline,wg_tcp_listen
#
# UDPLEX_TEST_IMAGE and UDPLEX_TEST_PLATFORM override the image and platform.
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$(dirname "$SCRIPT_DIR")")"
IMAGE="${UDPLEX_TEST_IMAGE:-golang:1.26-bookworm}"
PLATFORM="${UDPLEX_TEST_PLATFORM:-linux/$(docker version -f '{{.Server.Arch}}')}"

exec docker run --rm --privileged --platform "$PLATFORM" \
    -v "$PROJECT_ROOT:/src" \
    -v udplex-test-gomod:/go/pkg/mod \
    -v udplex-test-gocache:/root/.cache/go-build \
    -e GOFLAGS=-buildvcs=false \
    -w /src/tests/integration \
    "$IMAGE" \
    bash -c 'apt-get update -qq && apt-get install -y -qq iperf3 iproute2 >/dev/null && bash ./run_integration_tests.sh "$@"' _ "$@"
