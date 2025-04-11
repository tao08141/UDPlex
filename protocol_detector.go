package main

import (
	"bytes"
	"encoding/hex"
	"log"
)

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

	// Length as an object
	Length *LengthMatch `json:"length,omitempty"`

	// Pre-decoded data for performance
	decodedBytes    []byte
	decodedMask     []byte
	decodedContains []byte
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

// preprocess decodes hex strings in signature rules to improve performance
func preprocess(protoDefs map[string]ProtocolDefinition) map[string]ProtocolDefinition {
	processedDefs := make(map[string]ProtocolDefinition)

	for name, def := range protoDefs {
		processedDef := def

		for i, sig := range processedDef.Signatures {
			// Pre-decode Bytes field
			if sig.Bytes != "" {
				if sig.Hex {
					decoded, err := hex.DecodeString(sig.Bytes)
					if err == nil {
						processedDef.Signatures[i].decodedBytes = decoded
					} else {
						log.Printf("Error pre-decoding bytes pattern %s: %v", sig.Bytes, err)
					}
				} else {
					processedDef.Signatures[i].decodedBytes = []byte(sig.Bytes)
				}
			}

			// Pre-decode Mask field
			if sig.Mask != "" {
				if sig.Hex {
					decoded, err := hex.DecodeString(sig.Mask)
					if err == nil {
						processedDef.Signatures[i].decodedMask = decoded
					} else {
						log.Printf("Error pre-decoding mask %s: %v", sig.Mask, err)
					}
				} else {
					processedDef.Signatures[i].decodedMask = []byte(sig.Mask)
				}
			}

			// Pre-decode Contains field
			if sig.Contains != "" {
				if sig.Hex {
					decoded, err := hex.DecodeString(sig.Contains)
					if err == nil {
						processedDef.Signatures[i].decodedContains = decoded
					} else {
						log.Printf("Error pre-decoding contains pattern %s: %v", sig.Contains, err)
					}
				} else {
					processedDef.Signatures[i].decodedContains = []byte(sig.Contains)
				}
			}
		}

		processedDefs[name] = processedDef
	}

	return processedDefs
}

// NewProtocolDetector creates a new protocol detector
func NewProtocolDetector(protoDefs map[string]ProtocolDefinition) *ProtocolDetector {
	// Preprocess all protocol definitions to decode hex strings
	processedDefs := preprocess(protoDefs)

	return &ProtocolDetector{
		protocols: processedDefs,
	}
}

// DetectProtocol identifies the protocol of a UDP packet
func (pd *ProtocolDetector) DetectProtocol(data []byte, length int, useProtoDetectors []string) string {
	// Process protocols in priority order (if specified)
	// For simplicity, we'll just iterate through the map for now
	// for useProtoDetectors
	for _, protoName := range useProtoDetectors {
		if pd.matchProtocol(data, length, pd.protocols[protoName]) {
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
		pattern := sig.decodedBytes
		mask := sig.decodedMask

		// Check if offset is within bounds
		if sig.Offset+len(pattern) > length {
			return false
		}

		// Apply mask if present
		if len(mask) > 0 {
			for i := range pattern {
				if i >= len(mask) {
					break
				}

				if (data[sig.Offset+i] & mask[i]) != (pattern[i] & mask[i]) {
					return false
				}
			}
		} else {
			// Direct comparison without mask
			for i := range pattern {
				if data[sig.Offset+i] != pattern[i] {
					return false
				}
			}
		}
	}

	// Check contains
	if sig.Contains != "" {
		pattern := sig.decodedContains

		if !bytes.Contains(data[:length], pattern) {
			return false
		}
	}

	// All checks passed
	return true
}
