#!/bin/bash

# UDPlex Integration Test Runner
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$(dirname "$SCRIPT_DIR")")"

echo "=== UDPlex Integration Tests ==="
echo "Project root: $PROJECT_ROOT"
echo "Integration test directory: $SCRIPT_DIR"
echo

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: Go is not installed or not in PATH${NC}"
    exit 1
fi

# Check if UDPlex source exists
if [ ! -d "$PROJECT_ROOT/src" ]; then
    echo -e "${RED}Error: UDPlex source directory not found at $PROJECT_ROOT/src${NC}"
    exit 1
fi

# Check if examples exist
if [ ! -d "$PROJECT_ROOT/examples" ]; then
    echo -e "${RED}Error: Examples directory not found at $PROJECT_ROOT/examples${NC}"
    exit 1
fi

echo -e "${YELLOW}Building UDPlex...${NC}"
cd "$PROJECT_ROOT/src"
if go build -o "$PROJECT_ROOT/udplex_test" .; then
    echo -e "${GREEN}✓ UDPlex build successful${NC}"
else
    echo -e "${RED}✗ UDPlex build failed${NC}"
    exit 1
fi

echo
echo -e "${YELLOW}Running integration tests...${NC}"
cd "$SCRIPT_DIR"

# Kill any existing UDPlex processes
pkill -f "udplex_test" 2>/dev/null || true
sleep 1

# Run the integration test
if go run udp_integration.go; then
    echo
    echo -e "${GREEN}✓ All integration tests passed!${NC}"
    exit_code=0
else
    echo
    echo -e "${RED}✗ Some integration tests failed!${NC}"
    exit_code=1
fi

# Cleanup
echo -e "${YELLOW}Cleaning up...${NC}"
pkill -f "udplex_test" 2>/dev/null || true
rm -f "$PROJECT_ROOT/udplex_test" 2>/dev/null || true

exit $exit_code