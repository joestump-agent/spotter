package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// EncPrefixV1 is the versioned marker prepended to all new ciphertexts.
// It unambiguously distinguishes encrypted values from plaintext that merely
// looks like base64 (which the old length/shape heuristic misclassified).
// Governing: ADR-0006 (AES-256-GCM encryption), SPEC key-rotation
const EncPrefixV1 = "enc:v1:"

var (
	// ErrInvalidKey is returned when the encryption key is invalid
	ErrInvalidKey = errors.New("encryption key must be 32 bytes for AES-256")
	// ErrInvalidCiphertext is returned when attempting to decrypt invalid data
	ErrInvalidCiphertext = errors.New("invalid ciphertext")
	// ErrEncryptionKeyNotSet is returned when encryption key is not configured
	ErrEncryptionKeyNotSet = errors.New("encryption key not configured")
)

// Encryptor handles encryption and decryption of sensitive data
type Encryptor struct {
	key []byte
}

// NewEncryptor creates a new encryptor with the given 32-byte key for AES-256
func NewEncryptor(key []byte) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	return &Encryptor{key: key}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns a versioned,
// base64-encoded ciphertext.
// Format: enc:v1:base64(nonce || ciphertext || tag)
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Create a nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and authenticate
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode to base64 with the versioned marker for storage
	return EncPrefixV1 + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts ciphertext using AES-256-GCM. It accepts both the
// versioned enc:v1: format and the legacy bare-base64 format.
func (e *Encryptor) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	// Strip the versioned marker if present; legacy ciphertexts are bare base64.
	encoded := strings.TrimPrefix(ciphertext, EncPrefixV1)

	// Decode from base64
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: not base64 encoded", ErrInvalidCiphertext)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("%w: ciphertext too short", ErrInvalidCiphertext)
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}

	return string(plaintext), nil
}

// IsEncrypted reports whether a string carries the versioned enc:v1: ciphertext
// marker. Unlike the old base64-shape heuristic, this cannot misclassify
// plaintext that merely looks like base64. Legacy (bare base64) ciphertexts do
// not carry the marker; use Encryptor.IsCiphertext to also detect those.
// Governing: ADR-0006 (AES-256-GCM encryption)
func IsEncrypted(data string) bool {
	return strings.HasPrefix(data, EncPrefixV1)
}

// IsCiphertext reports whether value is a ciphertext readable by this
// encryptor: either it carries the enc:v1: marker, or it is a legacy bare
// base64 ciphertext that successfully decrypts with the encryptor's key.
// Plaintext that merely looks like base64 returns false.
func (e *Encryptor) IsCiphertext(value string) bool {
	if value == "" {
		return false
	}
	if IsEncrypted(value) {
		return true
	}
	// Legacy format: only treat as ciphertext if it actually decrypts.
	_, err := e.Decrypt(value)
	return err == nil
}

// DecryptAny decrypts a stored value that may be in the versioned enc:v1:
// format, the legacy bare-base64 format, or plaintext. It returns the
// plaintext and whether the value was actually encrypted.
//
// Marker-format values that fail to decrypt return an error (real corruption
// or a wrong key). Legacy-format values that fail to decrypt are treated as
// plaintext and returned as-is — this heals rows where plaintext was
// misclassified as ciphertext by the old base64-shape heuristic.
func (e *Encryptor) DecryptAny(value string) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}
	if IsEncrypted(value) {
		plaintext, err := e.Decrypt(value)
		if err != nil {
			return "", true, err
		}
		return plaintext, true, nil
	}
	// Legacy format: attempt decryption; on failure treat the value as plaintext.
	plaintext, err := e.Decrypt(value)
	if err != nil {
		return value, false, nil
	}
	return plaintext, true, nil
}

// EncryptInt encrypts an integer value and returns base64-encoded ciphertext
func (e *Encryptor) EncryptInt(value int) (string, error) {
	return e.Encrypt(fmt.Sprintf("%d", value))
}

// DecryptInt decrypts base64-encoded ciphertext and returns an integer value
func (e *Encryptor) DecryptInt(ciphertext string) (int, error) {
	plaintext, err := e.Decrypt(ciphertext)
	if err != nil {
		return 0, err
	}

	var value int
	if _, err := fmt.Sscanf(plaintext, "%d", &value); err != nil {
		return 0, fmt.Errorf("failed to parse decrypted integer: %w", err)
	}

	return value, nil
}
