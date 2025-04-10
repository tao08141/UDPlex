package main

import (
	"bytes"
	"encoding/hex"
	"log"
	"slices"
)

// BytesMatch represents a byte pattern to match at a specific offset
type BytesMatch struct {
	Offset int    `json:"offset"`          // Offset from the start of the packet
	Bytes  string `json:"bytes,omitempty"` // Hex string of bytes to match
	Mask   string `json:"mask,omitempty"`  // Hex string mask (FF means match exact byte)
	Hex    bool   `json:"hex"`             // Whether bytes/mask are in hex (true) or ASCII (false)
}

// ContainsMatch represents a pattern that should be contained anywhere in the packet
type ContainsMatch struct {
	Contains string `json:"contains"` // Bytes to search for
	Hex      bool   `json:"hex"`      // Whether contains is in hex (true) or ASCII (false)
}

// LengthMatch represents length requirements for the packet
type LengthMatch struct {
	Min int `json:"min,omitempty"` // Minimum length
	Max int `json:"max,omitempty"` // Maximum length
}

// SignatureRule represents a single matching rule
type SignatureRule struct {
	// Byte pattern matching at specific offset
	Offset int    `json:"offset,omitempty"`
	Bytes  string `json:"bytes,omitempty"`
	Mask   string `json:"mask,omitempty"`

	// Contains matching
	Contains string `json:"contains,omitempty"`
	Hex      bool   `json:"hex,omitempty"`

	// Length matching
	MinLength int `json:"min_length,omitempty"`
	MaxLength int `json:"max_length,omitempty"`

	// Length as an object
	Length *LengthMatch `json:"length,omitempty"`
}

// ProtocolDefinition represents a complete protocol detection rule
type ProtocolDefinition struct {
	Signatures  []SignatureRule `json:"signatures"`
	MatchLogic  string          `json:"match_logic"`
	Description string          `json:"description,omitempty"`
	Priority    int             `json:"priority,omitempty"`
}

// ProtocolDetector contains all protocol detection rules
type ProtocolDetector struct {
	protocols map[string]ProtocolDefinition
}

// NewProtocolDetector creates a new protocol detector
func NewProtocolDetector(protoDefs map[string]ProtocolDefinition) *ProtocolDetector {
	return &ProtocolDetector{
		protocols: protoDefs,
	}
}

// DetectProtocol identifies the protocol of a UDP packet
func (pd *ProtocolDetector) DetectProtocol(data []byte, length int, useProtoDetectors []string) string {
	// Process protocols in priority order (if specified)
	// For simplicity, we'll just iterate through the map for now
	for protoName, protoDef := range pd.protocols {

		found := slices.Contains(useProtoDetectors, protoName)
		if !found {
			continue // Skip this protocol if not in the list
		}

		if pd.matchProtocol(data, length, protoDef) {
			return protoName
		}
	}
	return "" // No protocol matched
}

// matchProtocol checks if a packet matches a protocol definition
func (pd *ProtocolDetector) matchProtocol(data []byte, length int, protoDef ProtocolDefinition) bool {
	matchAll := protoDef.MatchLogic == "AND"

	// If no signatures, return false
	if len(protoDef.Signatures) == 0 {
		return false
	}

	for _, sig := range protoDef.Signatures {
		matched := pd.matchSignature(data, length, sig)

		if matchAll && !matched {
			return false // AND logic: one failure means entire match fails
		}

		if !matchAll && matched {
			return true // OR logic: one success means entire match succeeds
		}
	}

	// If we get here with AND logic, all signatures matched
	// If we get here with OR logic, no signatures matched
	return matchAll
}

// matchSignature checks if a packet matches a single signature rule
func (pd *ProtocolDetector) matchSignature(data []byte, length int, sig SignatureRule) bool {
	// Check length constraints
	if sig.MinLength > 0 && length < sig.MinLength {
		return false
	}
	if sig.MaxLength > 0 && length > sig.MaxLength {
		return false
	}

	// Check length from Length object if present
	if sig.Length != nil {
		if sig.Length.Min > 0 && length < sig.Length.Min {
			return false
		}
		if sig.Length.Max > 0 && length > sig.Length.Max {
			return false
		}
	}

	// Check bytes at offset with mask
	if sig.Bytes != "" {
		var pattern []byte
		var mask []byte
		var err error

		if sig.Hex {
			pattern, err = hex.DecodeString(sig.Bytes)
			if err != nil {
				log.Printf("Error decoding bytes pattern %s: %v", sig.Bytes, err)
				return false
			}

			if sig.Mask != "" {
				mask, err = hex.DecodeString(sig.Mask)
				if err != nil {
					log.Printf("Error decoding mask %s: %v", sig.Mask, err)
					return false
				}
			}
		} else {
			pattern = []byte(sig.Bytes)
			if sig.Mask != "" {
				mask = []byte(sig.Mask)
			}
		}

		// Check if offset is within bounds
		if sig.Offset+len(pattern) > length {
			return false
		}

		// Apply mask if present
		if len(mask) > 0 {
			for i := 0; i < len(pattern); i++ {
				if i >= len(mask) {
					break
				}

				if (data[sig.Offset+i] & mask[i]) != (pattern[i] & mask[i]) {
					return false
				}
			}
		} else {
			// Direct comparison without mask
			for i := 0; i < len(pattern); i++ {
				if data[sig.Offset+i] != pattern[i] {
					return false
				}
			}
		}
	}

	// Check contains
	if sig.Contains != "" {
		var pattern []byte
		var err error

		if sig.Hex {
			pattern, err = hex.DecodeString(sig.Contains)
			if err != nil {
				log.Printf("Error decoding contains pattern %s: %v", sig.Contains, err)
				return false
			}
		} else {
			pattern = []byte(sig.Contains)
		}

		if !bytes.Contains(data[:length], pattern) {
			return false
		}
	}

	// All checks passed
	return true
}
