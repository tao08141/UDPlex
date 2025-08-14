package auth_test

import (
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"
)

// Mock auth functions for testing
func generateAuthKey(secret string, timestamp int64) []byte {
	h := sha256.New()
	h.Write([]byte(secret))
	h.Write([]byte{byte(timestamp), byte(timestamp >> 8), byte(timestamp >> 16), byte(timestamp >> 24)})
	return h.Sum(nil)
}

func validateAuthKey(secret string, key []byte, timestamp int64, window int) bool {
	// Check current timestamp and nearby timestamps within the window
	for i := -window; i <= window; i++ {
		testTimestamp := timestamp + int64(i)
		expectedKey := generateAuthKey(secret, testTimestamp)
		if compareKeys(key, expectedKey) {
			return true
		}
	}
	return false
}

func compareKeys(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func encryptData(data []byte, key []byte) []byte {
	// Simple XOR encryption for testing
	result := make([]byte, len(data))
	for i := range data {
		result[i] = data[i] ^ key[i%len(key)]
	}
	return result
}

func decryptData(encrypted []byte, key []byte) []byte {
	// XOR decryption is the same as encryption
	return encryptData(encrypted, key)
}

func TestGenerateAuthKey(t *testing.T) {
	secret := "test_secret"
	timestamp := time.Now().Unix()
	
	key1 := generateAuthKey(secret, timestamp)
	key2 := generateAuthKey(secret, timestamp)
	
	if !compareKeys(key1, key2) {
		t.Error("Same secret and timestamp should generate identical keys")
	}
	
	// Different timestamp should generate different key
	key3 := generateAuthKey(secret, timestamp+1)
	if compareKeys(key1, key3) {
		t.Error("Different timestamps should generate different keys")
	}
	
	// Different secret should generate different key
	key4 := generateAuthKey("different_secret", timestamp)
	if compareKeys(key1, key4) {
		t.Error("Different secrets should generate different keys")
	}
}

func TestValidateAuthKey(t *testing.T) {
	secret := "test_secret"
	timestamp := time.Now().Unix()
	window := 5
	
	// Valid key should pass validation
	validKey := generateAuthKey(secret, timestamp)
	if !validateAuthKey(secret, validKey, timestamp, window) {
		t.Error("Valid key should pass validation")
	}
	
	// Key within window should pass
	pastKey := generateAuthKey(secret, timestamp-3)
	if !validateAuthKey(secret, pastKey, timestamp, window) {
		t.Error("Key within window should pass validation")
	}
	
	futureKey := generateAuthKey(secret, timestamp+3)
	if !validateAuthKey(secret, futureKey, timestamp, window) {
		t.Error("Future key within window should pass validation")
	}
	
	// Key outside window should fail
	oldKey := generateAuthKey(secret, timestamp-10)
	if validateAuthKey(secret, oldKey, timestamp, window) {
		t.Error("Key outside window should fail validation")
	}
	
	// Wrong secret should fail
	wrongKey := generateAuthKey("wrong_secret", timestamp)
	if validateAuthKey(secret, wrongKey, timestamp, window) {
		t.Error("Key with wrong secret should fail validation")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	data := []byte("Hello, World! This is test data for encryption.")
	key := make([]byte, 32)
	rand.Read(key)
	
	encrypted := encryptData(data, key)
	decrypted := decryptData(encrypted, key)
	
	if string(decrypted) != string(data) {
		t.Errorf("Decrypted data = %q, want %q", string(decrypted), string(data))
	}
	
	// Encrypted data should be different from original
	if string(encrypted) == string(data) {
		t.Error("Encrypted data should be different from original")
	}
}

func TestEmptyData(t *testing.T) {
	secret := "test_secret"
	timestamp := time.Now().Unix()
	
	key := generateAuthKey(secret, timestamp)
	if len(key) == 0 {
		t.Error("Generated key should not be empty")
	}
	
	// Test with empty secret
	emptyKey := generateAuthKey("", timestamp)
	if len(emptyKey) == 0 {
		t.Error("Key should still be generated even with empty secret")
	}
	
	// Empty data encryption/decryption
	emptyData := []byte{}
	encryptedEmpty := encryptData(emptyData, key)
	decryptedEmpty := decryptData(encryptedEmpty, key)
	
	if len(decryptedEmpty) != 0 {
		t.Error("Decrypted empty data should remain empty")
	}
}

func TestKeyConsistency(t *testing.T) {
	secret := "consistency_test"
	timestamp := int64(1234567890)
	
	// Generate the same key multiple times
	keys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		keys[i] = generateAuthKey(secret, timestamp)
	}
	
	// All keys should be identical
	for i := 1; i < len(keys); i++ {
		if !compareKeys(keys[0], keys[i]) {
			t.Errorf("Key %d differs from key 0", i)
		}
	}
}