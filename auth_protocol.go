package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	ProtocolVersion = 1

	// Message Types
	MsgTypeAuthChallenge = 1
	MsgTypeAuthResponse  = 2
	MsgTypeHeartbeat     = 4
	MsgTypeData          = 5
	MsgTypeDisconnect    = 6

	// Protocol header size
	HeaderSize = 6

	// Auth message sizes
	ChallengeSize = 32
	TimestampSize = 8
	MACSize       = 32
	ResponseSize  = 32
	NonceSize     = 12

	// Timeouts
	DefaultAuthTimeout = 5 * time.Minute
	DefaultDataTimeout = 30 * time.Second
)

// ProtocolHeader represents the protocol header
type ProtocolHeader struct {
	Version  uint8
	MsgType  uint8
	Reserved uint16
	Length   uint16
}

// AuthState represents the authentication state for a connection
type AuthState struct {
	authenticated int32 // 0 = not authenticated, 1 = authenticated
	lastAuth      time.Time
	lastHeartbeat time.Time
	mu            sync.RWMutex
}

// AuthManager manages authentication and encryption
type AuthManager struct {
	secret            []byte
	enableEncryption  bool
	heartbeatInterval time.Duration
	authTimeout       time.Duration
	dataTimeout       time.Duration
	gcm               cipher.AEAD // Shared GCM cipher for performance
}

// NewAuthManager creates a new authentication manager
func NewAuthManager(config *AuthConfig) (*AuthManager, error) {
	if config == nil || !config.Enabled {
		return nil, nil
	}

	if len(config.Secret) == 0 {
		return nil, errors.New("auth secret cannot be empty")
	}

	// Create hash of secret for consistent 32-byte key
	hash := sha256.Sum256([]byte(config.Secret))
	secret := hash[:]

	var gcm cipher.AEAD
	if config.EnableEncryption {
		// Create AES cipher for encryption
		block, err := aes.NewCipher(secret[:16]) // Use first 16 bytes for AES-128
		if err != nil {
			return nil, fmt.Errorf("failed to create AES cipher: %w", err)
		}

		gcm, err = cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("failed to create GCM: %w", err)
		}
	}

	heartbeatInterval := time.Duration(config.HeartbeatInterval) * time.Second
	if heartbeatInterval == 0 {
		heartbeatInterval = 30 * time.Second
	}

	authTimeout := time.Duration(config.AuthTimeout) * time.Second
	if authTimeout == 0 {
		authTimeout = DefaultAuthTimeout
	}

	return &AuthManager{
		secret:            secret,
		enableEncryption:  config.EnableEncryption,
		heartbeatInterval: heartbeatInterval,
		authTimeout:       authTimeout,
		dataTimeout:       DefaultDataTimeout,
		gcm:               gcm,
	}, nil
}

// ParseHeader parses protocol header from buffer
func ParseHeader(buffer []byte) (*ProtocolHeader, error) {
	if len(buffer) < HeaderSize {
		return nil, errors.New("buffer too small for header")
	}

	return &ProtocolHeader{
		Version:  buffer[0],
		MsgType:  buffer[1],
		Reserved: binary.BigEndian.Uint16(buffer[2:4]),
		Length:   binary.BigEndian.Uint16(buffer[4:6]),
	}, nil
}

// WriteHeader writes protocol header to buffer
func WriteHeader(buffer []byte, msgType uint8, dataLen uint16) {
	buffer[0] = ProtocolVersion
	buffer[1] = msgType
	binary.BigEndian.PutUint16(buffer[2:4], 0) // Reserved
	binary.BigEndian.PutUint16(buffer[4:6], dataLen)
}

// CreateAuthChallenge creates an authentication challenge message
func (am *AuthManager) CreateAuthChallenge(buffer []byte, msgType uint8) (int, error) {
	// Generate challenge
	challenge := make([]byte, ChallengeSize)
	if _, err := rand.Read(challenge); err != nil {
		return 0, err
	}

	// Get current timestamp
	timestamp := time.Now().UnixMilli()
	timestampBytes := make([]byte, TimestampSize)
	binary.BigEndian.PutUint64(timestampBytes, uint64(timestamp))

	// Calculate HMAC
	h := hmac.New(sha256.New, am.secret)
	h.Write(challenge)
	h.Write(timestampBytes)
	mac := h.Sum(nil)

	// Write header
	dataLen := ChallengeSize + TimestampSize + MACSize
	WriteHeader(buffer, msgType, uint16(dataLen))

	// Write data
	offset := HeaderSize
	copy(buffer[offset:], challenge)
	offset += ChallengeSize
	copy(buffer[offset:], timestampBytes)
	offset += TimestampSize
	copy(buffer[offset:], mac)
	offset += MACSize

	return offset, nil
}

// ProcessAuthChallenge processes an authentication challenge (server side)
func (am *AuthManager) ProcessAuthChallenge(data []byte, authState *AuthState) error {
	if len(data) < ChallengeSize+TimestampSize+MACSize {
		return errors.New("invalid challenge data length")
	}

	challenge := data[:ChallengeSize]
	timestampBytes := data[ChallengeSize : ChallengeSize+TimestampSize]
	receivedMAC := data[ChallengeSize+TimestampSize : ChallengeSize+TimestampSize+MACSize]

	// Verify timestamp
	timestamp := int64(binary.BigEndian.Uint64(timestampBytes))
	if time.Since(time.UnixMilli(timestamp)) > am.authTimeout {
		return errors.New("challenge timestamp expired")
	}

	// Verify HMAC
	h := hmac.New(sha256.New, am.secret)
	h.Write(challenge)
	h.Write(timestampBytes)
	expectedMAC := h.Sum(nil)

	if !hmac.Equal(receivedMAC, expectedMAC) {
		return errors.New("invalid challenge MAC")
	}

	// Generate response
	response := make([]byte, ResponseSize)
	if _, err := rand.Read(response); err != nil {
		return err
	}

	return nil
}

// CreateHeartbeat creates a heartbeat message
func CreateHeartbeat(buffer []byte) int {
	WriteHeader(buffer, MsgTypeHeartbeat, 0)
	return HeaderSize
}

// WrapData wraps data in protocol format with optional encryption
func (am *AuthManager) WrapData(buffer []byte, data []byte) (int, error) {

	offset := HeaderSize
	var dataLen uint16

	if am.enableEncryption && am.gcm != nil {
		// Encrypted data format: Nonce(12) + EncryptedData(timestamp(8) + data)
		nonce := buffer[offset : offset+NonceSize]
		if _, err := rand.Read(nonce); err != nil {
			return 0, err
		}
		offset += NonceSize

		// Prepare plaintext: timestamp + data
		timestamp := time.Now().UnixMilli()
		plaintextLen := TimestampSize + len(data)
		plaintext := make([]byte, plaintextLen)
		binary.BigEndian.PutUint64(plaintext[:TimestampSize], uint64(timestamp))
		copy(plaintext[TimestampSize:], data)

		// Encrypt in place
		ciphertext := am.gcm.Seal(buffer[offset:offset], nonce, plaintext, nil)
		dataLen = NonceSize + uint16(len(ciphertext))
	} else {
		// Unencrypted data - direct copy
		// TODO zero-copy optimization
		copy(buffer[offset:], data)
		dataLen = uint16(len(data))
	}

	// Write header
	WriteHeader(buffer, MsgTypeData, dataLen)
	return HeaderSize + int(dataLen), nil
}

// UnwrapData unwraps protocol data with optional decryption
func (am *AuthManager) UnwrapData(buffer []byte, unwrappedData []byte) ([]byte, error) {

	header, err := ParseHeader(buffer)
	if err != nil {
		return nil, err
	}

	if header.Version != ProtocolVersion {
		return nil, errors.New("unsupported protocol version")
	}

	if header.MsgType != MsgTypeData {
		return nil, errors.New("not a data message")
	}

	data := buffer[HeaderSize : HeaderSize+header.Length]

	if am.enableEncryption && am.gcm != nil {
		if len(data) < NonceSize+TimestampSize+am.gcm.Overhead() {
			return nil, errors.New("encrypted data too short")
		}

		nonce := data[:NonceSize]
		ciphertext := data[NonceSize:]

		// Decrypt in place to avoid allocation
		plaintext, err := am.gcm.Open(unwrappedData[:0], nonce, ciphertext, nil)
		if err != nil {
			return nil, fmt.Errorf("decryption failed: %w", err)
		}

		if len(plaintext) < TimestampSize {
			return nil, errors.New("decrypted data too short")
		}

		// Verify timestamp
		timestamp := int64(binary.BigEndian.Uint64(plaintext[:TimestampSize]))
		if time.Since(time.UnixMilli(timestamp)) > am.dataTimeout {
			return nil, errors.New("data timestamp expired")
		}

		return plaintext[TimestampSize:], nil
	}

	return data, nil
}

// IsAuthenticated checks if connection is authenticated
func (authState *AuthState) IsAuthenticated() bool {
	return authState.authenticated == 1
}

// UpdateHeartbeat updates last heartbeat time
func (authState *AuthState) UpdateHeartbeat() {
	authState.mu.Lock()
	authState.lastHeartbeat = time.Now()
	authState.mu.Unlock()
}
