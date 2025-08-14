#!/bin/bash

# UDPlex Unit Test Runner
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

echo "=== UDPlex Unit Tests ==="
echo "Project root: $PROJECT_ROOT"
echo "Test directory: $SCRIPT_DIR"
echo

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test results
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# Function to run tests in a directory
run_test_dir() {
    local test_dir="$1"
    local test_name="$(basename "$test_dir")"
    local original_dir
    original_dir="$(pwd)"
    
    echo -e "${YELLOW}Running $test_name tests...${NC}"
    
    cd "$test_dir"
    
    if go test -v .; then
        echo -e "${GREEN}✓ $test_name tests passed${NC}"
        ((++PASSED_TESTS))
    else
        echo -e "${RED}✗ $test_name tests failed${NC}"
        ((++FAILED_TESTS))
    fi
    
    ((++TOTAL_TESTS))
    
    # Return to original directory for next iteration
    cd "$original_dir"
    echo
}

# Run unit tests for each component
cd "$SCRIPT_DIR"

echo -e "${YELLOW}=== Running Unit Tests ===${NC}"
for test_dir in unit/*/; do
    if [ -d "$test_dir" ]; then
        run_test_dir "$test_dir"
    fi
done

# Run integration tests
echo -e "${YELLOW}=== Running Integration Tests ===${NC}"
if [ -d "integration" ] && [ -f "integration/run_integration_tests.sh" ]; then
    echo -e "${YELLOW}Running integration tests...${NC}"
    cd integration
    if bash run_integration_tests.sh; then
        echo -e "${GREEN}✓ Integration tests passed${NC}"
        ((++PASSED_TESTS))
    else
        echo -e "${RED}✗ Integration tests failed${NC}"
        ((++FAILED_TESTS))
    fi
    ((++TOTAL_TESTS))
    cd ..
    echo
else
    echo -e "${YELLOW}Integration tests not found, skipping...${NC}"
fi

# Summary
echo "=== Test Summary ==="
echo "Total test suites: $TOTAL_TESTS"
echo -e "Passed: ${GREEN}$PASSED_TESTS${NC}"
echo -e "Failed: ${RED}$FAILED_TESTS${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}Some tests failed!${NC}"
    exit 1
fi