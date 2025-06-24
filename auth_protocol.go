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
	ProtocolVersion = 2

	// Message Types
	MsgTypeAuthChallenge = 1
	MsgTypeAuthResponse  = 2
	MsgTypeHeartbeat     = 4
	MsgTypeData          = 5
	MsgTypeDisconnect    = 6

	// Protocol header size
	HeaderSize = 8

	// Auth message sizes
	ChallengeSize = 32
	TimestampSize = 8
	MACSize       = 32
	ResponseSize  = 32
	NonceSize     = 12
	ForwardIDSize = 8
	PoolIDSize    = 8
	ConnIDSize    = 8

	// Deduplication constants
	FrameIDSize     = 8               // First 8 bytes of nonce
	SequenceSize    = 4               // Last 4 bytes of nonce
	BitmapSize      = 65535           // 2^32 bits for sequence numbers
	FrameTimeout    = 2 * time.Minute // Frame expiry time
	CleanupInterval = 1 * time.Minute // Cleanup frequency

	// Timeouts
	DefaultAuthTimeout = 30 * time.Second
	DefaultDataTimeout = 30 * time.Second
)

// FrameTracker tracks used sequence numbers for a frame
type FrameTracker struct {
	frameID    [FrameIDSize]byte
	bitmap     []uint64
	lastAccess time.Time
}

// DeduplicationManager manages frame tracking and deduplication
type DeduplicationManager struct {
	frames       map[[FrameIDSize]byte]*FrameTracker
	mu           sync.RWMutex
	currentFrame [FrameIDSize]byte
	currentSeq   uint32
	seqMu        sync.Mutex
	stopCleanup  chan struct{}
}

// NewDeduplicationManager creates a new deduplication manager
func NewDeduplicationManager() *DeduplicationManager {
	dm := &DeduplicationManager{
		frames:      make(map[[FrameIDSize]byte]*FrameTracker),
		stopCleanup: make(chan struct{}),
	}

	// Generate initial frame ID
	rand.Read(dm.currentFrame[:])

	// Start cleanup routine
	go dm.cleanupRoutine()

	return dm
}

// generateNextNonce generates the next nonce with frame ID and sequence
func (dm *DeduplicationManager) generateNextNonce() ([NonceSize]byte, error) {
	dm.seqMu.Lock()
	defer dm.seqMu.Unlock()

	var nonce [NonceSize]byte

	// Check if we need a new frame (sequence exhausted)
	if dm.currentSeq > BitmapSize-1 {
		// Generate new frame ID
		if _, err := rand.Read(dm.currentFrame[:]); err != nil {
			return nonce, err
		}
		dm.currentSeq = 0
	}

	// Copy frame ID (first 8 bytes)
	copy(nonce[:FrameIDSize], dm.currentFrame[:])

	// Set sequence number (last 4 bytes)
	binary.BigEndian.PutUint32(nonce[FrameIDSize:], dm.currentSeq)

	dm.currentSeq++

	return nonce, nil
}

// markAsUsed marks a nonce as used after validation
func (dm *DeduplicationManager) markAsUsed(nonce [NonceSize]byte) {
	var frameID [FrameIDSize]byte
	copy(frameID[:], nonce[:FrameIDSize])

	sequence := binary.BigEndian.Uint32(nonce[FrameIDSize:])

	dm.mu.Lock()
	defer dm.mu.Unlock()

	tracker, exists := dm.frames[frameID]
	if !exists {
		// New frame, create tracker
		tracker = &FrameTracker{
			frameID:    frameID,
			bitmap:     make([]uint64, (BitmapSize+63)/64),
			lastAccess: time.Now(),
		}
		dm.frames[frameID] = tracker
	}

	// Update last access time
	tracker.lastAccess = time.Now()

	// Check if sequence number is within our bitmap range
	if sequence >= BitmapSize {
		return // Sequence too large, ignore
	}

	// Calculate which uint64 and which bit within that uint64
	wordIndex := sequence / 64
	bitIndex := sequence % 64

	// Mark this sequence number as used
	tracker.bitmap[wordIndex] |= (1 << bitIndex)
}

// isDuplicate checks if a nonce represents a duplicate packet without marking it as used
func (dm *DeduplicationManager) isDuplicate(nonce [NonceSize]byte) bool {
	var frameID [FrameIDSize]byte
	copy(frameID[:], nonce[:FrameIDSize])

	sequence := binary.BigEndian.Uint32(nonce[FrameIDSize:])

	dm.mu.RLock()
	defer dm.mu.RUnlock()

	tracker, exists := dm.frames[frameID]
	if !exists {
		return false // New frame, not a duplicate
	}

	// Check if sequence number is within our bitmap range
	if sequence >= BitmapSize {
		return false // Sequence too large, consider it new
	}

	// Calculate which uint64 and which bit within that uint64
	wordIndex := sequence / 64
	bitIndex := sequence % 64

	// Check if this sequence number has been used
	return tracker.bitmap[wordIndex]&(1<<bitIndex) != 0
}

// cleanupRoutine periodically removes expired frames
func (dm *DeduplicationManager) cleanupRoutine() {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.cleanupExpiredFrames()
		case <-dm.stopCleanup:
			return
		}
	}
}

// cleanupExpiredFrames removes frames that haven't been accessed recently
func (dm *DeduplicationManager) cleanupExpiredFrames() {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	cutoff := time.Now().Add(-FrameTimeout)

	for frameID, tracker := range dm.frames {
		expired := tracker.lastAccess.Before(cutoff)

		if expired {
			delete(dm.frames, frameID)
		}
	}
}

// Stop stops the deduplication manager
func (dm *DeduplicationManager) Stop() {
	close(dm.stopCleanup)
}

// ProtocolHeader represents the protocol header
type ProtocolHeader struct {
	Version  uint8
	MsgType  uint8
	Reserved uint16
	Length   uint32
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
	router            *Router
	gcm               cipher.AEAD // Shared GCM cipher for performance
	deduplicationMgr  *DeduplicationManager
}

// NewAuthManager creates a new authentication manager
func NewAuthManager(config *AuthConfig, router *Router) (*AuthManager, error) {
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
		router:            router,
		deduplicationMgr:  NewDeduplicationManager(),
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
		Length:   binary.BigEndian.Uint32(buffer[4:8]),
	}, nil
}

// WriteHeader writes protocol header to buffer
func WriteHeader(buffer []byte, msgType uint8, dataLen uint32) {
	buffer[0] = ProtocolVersion
	buffer[1] = msgType
	binary.BigEndian.PutUint16(buffer[2:4], 0) // Reserved
	binary.BigEndian.PutUint32(buffer[4:8], dataLen)
}

// CreateAuthChallenge creates an authentication challenge message
func (am *AuthManager) CreateAuthChallenge(buffer []byte, msgType uint8, forwardID ForwardID, poolID PoolID) (int, error) {
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
	h.Write(forwardID[:])
	h.Write(poolID[:])
	h.Write(timestampBytes)
	mac := h.Sum(nil)

	// Write header
	dataLen := ChallengeSize + TimestampSize + ForwardIDSize + PoolIDSize + MACSize
	WriteHeader(buffer, msgType, uint32(dataLen))

	// Write data
	offset := HeaderSize
	copy(buffer[offset:], challenge)
	offset += ChallengeSize
	copy(buffer[offset:], forwardID[:])
	offset += ForwardIDSize
	copy(buffer[offset:], poolID[:])
	offset += PoolIDSize
	copy(buffer[offset:], timestampBytes)
	offset += TimestampSize
	copy(buffer[offset:], mac)
	offset += MACSize

	return offset, nil
}

// ProcessAuthChallenge processes an authentication challenge (server side)
func (am *AuthManager) ProcessAuthChallenge(data []byte, authState *AuthState) error {
	if len(data) < ChallengeSize+TimestampSize+ForwardIDSize+PoolIDSize+MACSize {
		return errors.New("invalid challenge data length")
	}

	challenge := data[:ChallengeSize]
	ForwardID := ForwardIDFromBytes(data[ChallengeSize : ChallengeSize+ForwardIDSize])
	poolID := ForwardIDFromBytes(data[ChallengeSize+ForwardIDSize : ChallengeSize+ForwardIDSize+PoolIDSize])
	timestampBytes := data[ChallengeSize+ForwardIDSize+PoolIDSize : ChallengeSize+ForwardIDSize+PoolIDSize+TimestampSize]
	receivedMAC := data[ChallengeSize+ForwardIDSize+PoolIDSize+TimestampSize : ChallengeSize+ForwardIDSize+PoolIDSize+TimestampSize+MACSize]

	// Verify timestamp
	timestamp := int64(binary.BigEndian.Uint64(timestampBytes))
	if time.Since(time.UnixMilli(timestamp)) > am.authTimeout {
		return errors.New("challenge timestamp expired")
	}

	// Verify HMAC
	h := hmac.New(sha256.New, am.secret)
	h.Write(challenge)
	h.Write(ForwardID[:])
	h.Write(poolID[:])
	h.Write(timestampBytes)
	expectedMAC := h.Sum(nil)

	if !hmac.Equal(receivedMAC, expectedMAC) {
		return errors.New("invalid challenge MAC")
	}

	return nil
}

// CreateHeartbeat creates a heartbeat message
func CreateHeartbeat(buffer []byte) int {
	WriteHeader(buffer, MsgTypeHeartbeat, 0)
	return HeaderSize
}

// WrapData wraps data in protocol format with optional encryption
func (am *AuthManager) WrapData(packet *Packet) error {
	if am.enableEncryption && am.gcm != nil {
		// Encrypted data format: Header + Nonce(12) + EncryptedData(connID(8) + timestamp(8) + originalData)

		// Prepare plaintext: connID + timestamp + original data
		neededSpace := ConnIDSize + TimestampSize
		offset := HeaderSize

		if packet.offset > neededSpace {
			// Shift existing data to make space for connID and timestamp
			packet.offset -= neededSpace
		} else {
			newBuffer := am.router.GetBuffer()
			copy(newBuffer[neededSpace:], packet.GetData())
			packet.SetBuffer(newBuffer[:packet.length])
			packet.offset = 0
		}

		timestamp := time.Now().UnixMilli()
		binary.BigEndian.PutUint64(packet.buffer[packet.offset:], uint64(timestamp))
		binary.BigEndian.PutUint64(packet.buffer[packet.offset+TimestampSize:], packet.connID.ToUint64())

		packet.length += neededSpace

		// Get a new buffer for the wrapped packet
		buffer := am.router.GetBuffer()

		// Generate nonce with deduplication
		nonce, err := am.deduplicationMgr.generateNextNonce()
		if err != nil {
			return err
		}
		copy(buffer[offset:offset+NonceSize], nonce[:])

		offset += NonceSize

		// Encrypt
		ciphertext := am.gcm.Seal(buffer[offset:offset], nonce[:], packet.GetData(), nil)
		if len(ciphertext) == 0 {
			return errors.New("encryption failed, ciphertext is empty")
		}

		// Only mark nonce as used after successful encryption
		am.deduplicationMgr.markAsUsed(nonce)

		totalDataLen := NonceSize + len(ciphertext)
		WriteHeader(buffer, MsgTypeData, uint32(totalDataLen))

		packet.SetBuffer(buffer[:HeaderSize+totalDataLen])
		packet.offset = 0
		packet.length = HeaderSize + totalDataLen

	} else {
		// Unencrypted data format: Header + connID(8) + originalData

		if packet.offset >= HeaderSize+ConnIDSize {
			// Shift header and connID before existing data
			packet.offset -= (HeaderSize + ConnIDSize)
		} else {
			// Need new buffer
			buffer := packet.router.GetBuffer()
			copy(buffer[HeaderSize+ConnIDSize:], packet.buffer[packet.offset:packet.offset+packet.length])

			packet.SetBuffer(buffer)
			packet.offset = 0
		}

		// Add header
		WriteHeader(packet.buffer[packet.offset:], MsgTypeData, uint32(packet.length+ConnIDSize))

		// Add connID after header
		binary.BigEndian.PutUint64(packet.buffer[packet.offset+HeaderSize:], packet.connID.ToUint64())

		packet.length += (HeaderSize + ConnIDSize)
	}

	return nil
}

// UnwrapData unwraps protocol data with optional decryption
func (am *AuthManager) UnwrapData(packet *Packet) (*ProtocolHeader, error) {
	if packet.length < HeaderSize {
		return nil, errors.New("packet too small for header")
	}

	header, err := ParseHeader(packet.buffer[packet.offset:])
	if err != nil {
		return header, err
	}

	if header.Version != ProtocolVersion {
		return header, errors.New("unsupported protocol version")
	}

	if header.MsgType != MsgTypeData {
		return header, nil
	}

	totalExpectedLen := HeaderSize + int(header.Length)
	if packet.length < totalExpectedLen {
		return header, fmt.Errorf("packet too small for declared data length (got %d, need %d)",
			packet.length, totalExpectedLen)
	}

	dataOffset := packet.offset + HeaderSize
	dataLen := int(header.Length)

	data := packet.buffer[dataOffset : dataOffset+dataLen]

	if am.enableEncryption && am.gcm != nil {
		if len(data) < NonceSize+TimestampSize+ConnIDSize+am.gcm.Overhead() {
			return header, errors.New("encrypted data too short")
		}

		var nonce [NonceSize]byte
		copy(nonce[:], data[:NonceSize])

		// Check for duplicates without marking as used
		if am.deduplicationMgr.isDuplicate(nonce) {
			return header, errors.New("duplicate packet detected")
		}

		ciphertext := data[NonceSize:]

		plaintext := am.router.GetBuffer()

		// Decrypt
		plaintext, err := am.gcm.Open(plaintext[:0], nonce[:], ciphertext, nil)
		if err != nil {
			return header, fmt.Errorf("decryption failed: %w", err)
		}

		if len(plaintext) < (ConnIDSize + TimestampSize) {
			return header, errors.New("decrypted data too short")
		}

		// Verify timestamp
		timestamp := int64(binary.BigEndian.Uint64(plaintext[:TimestampSize]))
		if time.Since(time.UnixMilli(timestamp)) > am.dataTimeout {
			return header, errors.New("data timestamp expired " + fmt.Sprintf("%d %d ", timestamp, time.Now().UnixMilli()))
		}

		// Extract connID
		connIDVal := binary.BigEndian.Uint64(plaintext[TimestampSize : ConnIDSize+TimestampSize])
		packet.connID = ConnIDFromUint64(connIDVal)

		// Only mark nonce as used after successful decryption and validation
		am.deduplicationMgr.markAsUsed(nonce)

		packet.SetBuffer(plaintext)
		packet.offset = ConnIDSize + TimestampSize // Skip connID and timestamp
		packet.length = len(plaintext) - (ConnIDSize + TimestampSize)

	} else {
		// Unencrypted: extract connID and skip header and connID
		if dataLen < ConnIDSize {
			return header, errors.New("data too short to contain connID")
		}

		packet.connID = ConnIDFromUint64(binary.BigEndian.Uint64(data[:ConnIDSize]))
		packet.offset = dataOffset + ConnIDSize // Skip header and connID
		packet.length = dataLen - ConnIDSize    // Subtract connID size from data length
	}

	return header, nil
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
