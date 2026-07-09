package crypto

import (
	"crypto/rand"
	"strings"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	// Generate a random 32-byte key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{"simple password", "mypassword123"},
		{"empty string", ""},
		{"special characters", "p@ssw0rd!#$%^&*()"},
		{"unicode", "пароль密码🔐"},
		{"long text", "this is a much longer password with many characters to test the encryption of larger strings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encrypt
			ciphertext, err := encryptor.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt() error = %v", err)
			}

			// Empty strings should stay empty
			if tt.plaintext == "" && ciphertext != "" {
				t.Errorf("Encrypt('') should return '', got %q", ciphertext)
			}
			if tt.plaintext != "" && ciphertext == "" {
				t.Errorf("Encrypt(%q) returned empty string", tt.plaintext)
			}

			// Decrypt
			decrypted, err := encryptor.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("Decrypt() error = %v", err)
			}

			// Verify
			if decrypted != tt.plaintext {
				t.Errorf("Decrypt() = %q, want %q", decrypted, tt.plaintext)
			}
		})
	}
}

func TestEncryptionUniqueness(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	plaintext := "samepassword"

	// Encrypt the same plaintext multiple times
	ciphertext1, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	ciphertext2, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Ciphertexts should be different (due to random nonce)
	if ciphertext1 == ciphertext2 {
		t.Errorf("Encrypting same plaintext twice produced identical ciphertext (nonce not random)")
	}

	// But both should decrypt to the same plaintext
	decrypted1, _ := encryptor.Decrypt(ciphertext1)
	decrypted2, _ := encryptor.Decrypt(ciphertext2)

	if decrypted1 != plaintext || decrypted2 != plaintext {
		t.Errorf("Decrypted texts don't match original")
	}
}

func TestInvalidKey(t *testing.T) {
	tests := []struct {
		name    string
		keySize int
		wantErr bool
	}{
		{"16 bytes (AES-128)", 16, true},
		{"24 bytes (AES-192)", 24, true},
		{"32 bytes (AES-256)", 32, false},
		{"0 bytes", 0, true},
		{"64 bytes", 64, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := make([]byte, tt.keySize)
			_, err := NewEncryptor(key)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewEncryptor() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDecryptInvalid(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	tests := []struct {
		name       string
		ciphertext string
		wantErr    bool
	}{
		{"empty string", "", false}, // Empty is allowed
		{"not base64", "not-base64-data!@#", true},
		{"too short", "YWJj", true},                             // "abc" in base64, too short for GCM
		{"random base64", "YWJjZGVmZ2hpamtsbW5vcHFyc3Q=", true}, // Valid base64 but invalid ciphertext
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := encryptor.Decrypt(tt.ciphertext)
			if (err != nil) != tt.wantErr {
				t.Errorf("Decrypt() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsEncrypted(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	// Encrypt a password
	encrypted, err := encryptor.Encrypt("mypassword")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Legacy ciphertext: same payload without the enc:v1: marker.
	legacy := strings.TrimPrefix(encrypted, EncPrefixV1)

	tests := []struct {
		name string
		data string
		want bool
	}{
		{"encrypted data", encrypted, true},
		{"legacy ciphertext without marker", legacy, false},
		{"plaintext password", "mypassword", false},
		{"empty string", "", false},
		{"short base64", "YWJj", false},
		{"not base64", "plain text password!", false},
		// Base64-shaped plaintext that the old length heuristic misclassified.
		{"base64-shaped plaintext", "dGhpcyBpcyBqdXN0IGEgbG9uZyBwbGFpbnRleHQgdmFsdWU=", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEncrypted(tt.data); got != tt.want {
				t.Errorf("IsEncrypted() = %v, want %v for %q", got, tt.want, tt.data)
			}
		})
	}
}

// TestEncryptWritesMarker verifies that new ciphertexts carry the enc:v1: marker.
func TestEncryptWritesMarker(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	ciphertext, err := encryptor.Encrypt("some secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !strings.HasPrefix(ciphertext, EncPrefixV1) {
		t.Errorf("Encrypt() output %q does not carry the %q marker", ciphertext, EncPrefixV1)
	}
	if !IsEncrypted(ciphertext) {
		t.Errorf("IsEncrypted() = false for freshly encrypted value")
	}
}

// TestLegacyCiphertextStillDecrypts verifies backward compatibility with
// ciphertexts written before the enc:v1: marker was introduced.
func TestLegacyCiphertextStillDecrypts(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	plaintext := "legacy secret"
	ciphertext, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	legacy := strings.TrimPrefix(ciphertext, EncPrefixV1)

	// Decrypt accepts the bare-base64 legacy format.
	decrypted, err := encryptor.Decrypt(legacy)
	if err != nil {
		t.Fatalf("Decrypt(legacy) error = %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("Decrypt(legacy) = %q, want %q", decrypted, plaintext)
	}

	// IsCiphertext recognizes legacy ciphertext via trial decryption.
	if !encryptor.IsCiphertext(legacy) {
		t.Errorf("IsCiphertext(legacy) = false, want true")
	}

	// DecryptAny also handles it and reports it as encrypted.
	decrypted, wasEncrypted, err := encryptor.DecryptAny(legacy)
	if err != nil {
		t.Fatalf("DecryptAny(legacy) error = %v", err)
	}
	if !wasEncrypted {
		t.Errorf("DecryptAny(legacy) wasEncrypted = false, want true")
	}
	if decrypted != plaintext {
		t.Errorf("DecryptAny(legacy) = %q, want %q", decrypted, plaintext)
	}
}

// TestBase64ShapedPlaintextRoundTrip verifies that plaintext which merely
// looks like base64 (>= 29 decoded bytes, the old heuristic's false positive)
// is treated as plaintext on read and still gets encrypted on write.
func TestBase64ShapedPlaintextRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	encryptor, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	// Valid base64 decoding to > 29 bytes — the old heuristic called this encrypted.
	plaintext := "dGhpcyBpcyBqdXN0IGEgbG9uZyBwbGFpbnRleHQgdmFsdWU="

	// It must not be classified as ciphertext, so the encrypt hook encrypts it.
	if encryptor.IsCiphertext(plaintext) {
		t.Errorf("IsCiphertext(base64-shaped plaintext) = true, want false")
	}

	// On read, DecryptAny heals the false positive: returns the value as-is.
	got, wasEncrypted, err := encryptor.DecryptAny(plaintext)
	if err != nil {
		t.Fatalf("DecryptAny() error = %v", err)
	}
	if wasEncrypted {
		t.Errorf("DecryptAny() wasEncrypted = true, want false")
	}
	if got != plaintext {
		t.Errorf("DecryptAny() = %q, want %q", got, plaintext)
	}

	// Full round-trip: encrypt then decrypt yields the original value.
	ciphertext, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !IsEncrypted(ciphertext) {
		t.Errorf("Encrypt() output missing enc:v1: marker")
	}
	decrypted, err := encryptor.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("round-trip = %q, want %q", decrypted, plaintext)
	}
}

// TestDecryptAnyMarkerWrongKey verifies that a marker-format ciphertext which
// fails to decrypt returns an error (real corruption or wrong key) instead of
// being silently treated as plaintext.
func TestDecryptAnyMarkerWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	if _, err := rand.Read(key1); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	if _, err := rand.Read(key2); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	enc1, err := NewEncryptor(key1)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}
	enc2, err := NewEncryptor(key2)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	ciphertext, err := enc1.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	if _, _, err := enc2.DecryptAny(ciphertext); err == nil {
		t.Error("DecryptAny() with wrong key on marker-format ciphertext should error")
	}

	// A legacy ciphertext under the wrong key is indistinguishable from
	// plaintext and must be returned as-is instead of erroring.
	legacy := strings.TrimPrefix(ciphertext, EncPrefixV1)
	got, wasEncrypted, err := enc2.DecryptAny(legacy)
	if err != nil {
		t.Fatalf("DecryptAny(legacy, wrong key) error = %v", err)
	}
	if wasEncrypted {
		t.Errorf("DecryptAny(legacy, wrong key) wasEncrypted = true, want false")
	}
	if got != legacy {
		t.Errorf("DecryptAny(legacy, wrong key) = %q, want value returned as-is", got)
	}
}
