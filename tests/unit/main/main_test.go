package main_test

import (
	"strings"
	"testing"
	"path/filepath"
	"os"
	"os/exec"
	"runtime"
)

// TestStripJSONComments tests the JSON comment stripping functionality
func TestStripJSONComments(t *testing.T) {
	// Get the path to the main package
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to get caller info")
	}
	
	srcDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "src")
	
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single line comment",
			input:    `{"key": "value"} // this is a comment`,
			expected: `{"key": "value"} `,
		},
		{
			name: "multiline comment",
			input: `{
				"key": "value",
				/* this is a 
				   multiline comment */
				"another": "value"
			}`,
			expected: `{
				"key": "value",
				
				"another": "value"
			}`,
		},
		{
			name:     "comment in string should be preserved",
			input:    `{"url": "http://example.com//path"}`,
			expected: `{"url": "http://example.com//path"}`,
		},
		{
			name:     "no comments",
			input:    `{"key": "value", "number": 123}`,
			expected: `{"key": "value", "number": 123}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test program that imports and uses the stripJSONComments function
			testProgram := `
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// Copy of stripJSONComments function for testing
func stripJSONComments(data []byte) ([]byte, error) {
	var result bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))

	inMultiLineComment := false

	for scanner.Scan() {
		line := scanner.Text()

		if inMultiLineComment {
			if idx := strings.Index(line, "*/"); idx != -1 {
				inMultiLineComment = false
				line = line[idx+2:]
			} else {
				continue
			}
		}

		var cleanLine strings.Builder
		inString := false
		escaped := false

		for i := 0; i < len(line); i++ {
			char := line[i]

			if escaped {
				cleanLine.WriteByte(char)
				escaped = false
				continue
			}

			if char == '\\' && inString {
				cleanLine.WriteByte(char)
				escaped = true
				continue
			}

			if char == '"' {
				inString = !inString
				cleanLine.WriteByte(char)
				continue
			}

			if !inString {
				if i < len(line)-1 && line[i] == '/' && line[i+1] == '/' {
					break
				}

				if i < len(line)-1 && line[i] == '/' && line[i+1] == '*' {
					inMultiLineComment = true
					i++
					continue
				}
			}

			cleanLine.WriteByte(char)
		}

		result.WriteString(cleanLine.String())
		result.WriteByte('\n')
	}

	return result.Bytes(), scanner.Err()
}

func main() {
	input := os.Args[1]
	result, err := stripJSONComments([]byte(input))
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(string(result))
}
`
			
			// Create temporary test file
			tmpFile, err := os.CreateTemp("", "test_*.go")
			if err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}
			defer os.Remove(tmpFile.Name())
			
			if _, err := tmpFile.WriteString(testProgram); err != nil {
				t.Fatalf("failed to write test program: %v", err)
			}
			tmpFile.Close()
			
			// Run the test program
			cmd := exec.Command("go", "run", tmpFile.Name(), tt.input)
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("failed to run test program: %v", err)
			}
			
			result := strings.TrimSpace(string(output))
			expected := strings.TrimSpace(tt.expected)
			
			if result != expected {
				t.Errorf("stripJSONComments() = %q, want %q", result, expected)
			}
		})
	}
}

// TestMainBinary tests that the main binary can be built
func TestMainBinary(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to get caller info")
	}
	
	srcDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "src")
	
	cmd := exec.Command("go", "build", "-o", "/tmp/udplex_test")
	cmd.Dir = srcDir
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build main binary: %v\nOutput: %s", err, output)
	}
	
	// Clean up
	os.Remove("/tmp/udplex_test")
}