# UDPlex Makefile

.PHONY: all build test test-unit test-integration clean help

# Default target
all: build test

# Build the UDPlex binary
build:
	@echo "Building UDPlex..."
	@cd src && go build -o ../udplex .
	@echo "✓ Build complete"

# Build for testing
build-test:
	@echo "Building UDPlex for testing..."
	@cd src && go build -o ../udplex_test .
	@echo "✓ Test build complete"

# Run all tests
test: test-unit test-integration

# Run only unit tests
test-unit:
	@echo "Running unit tests..."
	@cd tests && chmod +x run_tests.sh && ./run_tests.sh

# Run only integration tests
test-integration: build-test
	@echo "Running integration tests..."
	@cd tests/integration && chmod +x run_integration_tests.sh && ./run_integration_tests.sh

# Clean build artifacts
clean:
	@echo "Cleaning up..."
	@rm -f udplex udplex_test
	@rm -rf dist/
	@echo "✓ Clean complete"

# Show help
help:
	@echo "UDPlex Build System"
	@echo ""
	@echo "Available targets:"
	@echo "  all              - Build and run all tests"
	@echo "  build            - Build the UDPlex binary"
	@echo "  build-test       - Build UDPlex for testing"
	@echo "  test             - Run all tests (unit + integration)"
	@echo "  test-unit        - Run only unit tests"
	@echo "  test-integration - Run only integration tests"
	@echo "  clean            - Clean build artifacts"
	@echo "  help             - Show this help message"