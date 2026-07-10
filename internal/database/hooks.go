package database

import (
	"context"
	"fmt"

	"spotter/ent"
	"spotter/ent/hook"
	"spotter/internal/crypto"
)

// RegisterEncryptionHooks registers hooks to encrypt/decrypt sensitive data
// Governing: ADR-0006 (application-layer AES-256-GCM encryption via Ent hooks)
func RegisterEncryptionHooks(client *ent.Client, encryptor *crypto.Encryptor) {
	// Hook for encrypting password on NavidromeAuth create/update
	client.NavidromeAuth.Use(encryptPasswordHook(encryptor))

	// Hook for decrypting password on NavidromeAuth query
	client.NavidromeAuth.Intercept(decryptPasswordInterceptor(client, encryptor))

	// Hook for encrypting tokens on SpotifyAuth create/update
	client.SpotifyAuth.Use(encryptSpotifyAuthHook(encryptor))

	// Hook for decrypting tokens on SpotifyAuth query
	client.SpotifyAuth.Intercept(decryptSpotifyAuthInterceptor(client, encryptor))

	// Hook for encrypting session key on LastFMAuth create/update
	client.LastFMAuth.Use(encryptLastFMAuthHook(encryptor))

	// Hook for decrypting session key on LastFMAuth query
	client.LastFMAuth.Intercept(decryptLastFMAuthInterceptor(client, encryptor))
}

// resolveEncryptedField returns the plaintext for a stored credential value
// and, when the stored form is legacy, a marked ciphertext to write back
// (self-heal). Resolution order:
//
//  1. Explicit marker (enc:v1:) → decrypt; a failure here is a real error
//     (wrong key or corrupted ciphertext) and is returned to the caller.
//  2. Legacy ciphertext shape (bare base64, >= 29 decoded bytes) → attempt
//     decryption. Success means it is pre-marker ciphertext; GCM failure
//     means it is plaintext that merely looks like ciphertext (e.g. a
//     40-char alphanumeric password). Either way the row self-heals: the
//     plaintext is re-encrypted with the marker and returned as healed.
//     This path NEVER returns an error — previously it bricked the auth row
//     by failing the whole query (issue #335).
//  3. Anything else is plaintext from before encryption was enabled; it is
//     left as-is and will be encrypted on the next write.
//
// Governing: ADR-0006
func resolveEncryptedField(encryptor *crypto.Encryptor, stored string) (plaintext, healed string, err error) {
	if stored == "" {
		return "", "", nil
	}

	if crypto.IsEncrypted(stored) {
		decrypted, err := encryptor.Decrypt(stored)
		if err != nil {
			return "", "", err
		}
		return decrypted, "", nil
	}

	if crypto.LooksLikeLegacyCiphertext(stored) {
		if decrypted, err := encryptor.Decrypt(stored); err == nil {
			// Legacy ciphertext: migrate to the marked format.
			if reencrypted, err := encryptor.Encrypt(decrypted); err == nil {
				return decrypted, reencrypted, nil
			}
			return decrypted, "", nil
		}
		// GCM failure: the value is plaintext that merely looks like
		// ciphertext. Treat it as plaintext and self-heal by encrypting
		// it with the marker so future reads are unambiguous.
		if reencrypted, err := encryptor.Encrypt(stored); err == nil {
			return stored, reencrypted, nil
		}
		return stored, "", nil
	}

	// Plaintext from before encryption was enabled (will be encrypted on next write)
	return stored, "", nil
}

// encryptPasswordHook encrypts the password field before saving to database
// and decrypts it in the returned entity
func encryptPasswordHook(encryptor *crypto.Encryptor) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.NavidromeAuthFunc(func(ctx context.Context, m *ent.NavidromeAuthMutation) (ent.Value, error) {
			// Remember original password for decryption after save
			var originalPassword string
			if password, exists := m.Password(); exists {
				originalPassword = password
				// Check if already encrypted (for idempotency)
				if !crypto.IsEncrypted(password) {
					// Encrypt the password
					encrypted, err := encryptor.Encrypt(password)
					if err != nil {
						return nil, fmt.Errorf("failed to encrypt password: %w", err)
					}
					m.SetPassword(encrypted)
				}
			}

			// Execute the mutation
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return nil, err
			}

			// Decrypt the password in the returned entity
			if auth, ok := value.(*ent.NavidromeAuth); ok && originalPassword != "" {
				auth.Password = originalPassword
			}

			return value, nil
		})
	}
}

// decryptPasswordInterceptor decrypts password fields after loading from database
func decryptPasswordInterceptor(client *ent.Client, encryptor *crypto.Encryptor) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			// Execute the query
			v, err := next.Query(ctx, q)
			if err != nil {
				return nil, err
			}

			// Decrypt passwords in the results
			switch result := v.(type) {
			case *ent.NavidromeAuth:
				if err := decryptNavidromeAuth(ctx, client, encryptor, result); err != nil {
					return nil, err
				}
			case []*ent.NavidromeAuth:
				for _, auth := range result {
					if err := decryptNavidromeAuth(ctx, client, encryptor, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptNavidromeAuth decrypts the password field in a NavidromeAuth entity,
// self-healing legacy-format rows to the marked ciphertext format.
func decryptNavidromeAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, auth *ent.NavidromeAuth) error {
	if auth == nil || auth.Password == "" {
		return nil
	}

	plaintext, healed, err := resolveEncryptedField(encryptor, auth.Password)
	if err != nil {
		return fmt.Errorf("failed to decrypt password for user %d: %w", auth.ID, err)
	}
	if healed != "" {
		// Best-effort self-heal: persist the marked ciphertext. The write
		// hook sees the marker and stores it as-is. Failures are ignored —
		// the read must never break, and the next read retries the heal.
		_ = client.NavidromeAuth.UpdateOneID(auth.ID).SetPassword(healed).Exec(ctx)
	}
	auth.Password = plaintext

	return nil
}

// encryptSpotifyAuthHook encrypts the access_token and refresh_token fields before saving to database
// and decrypts them in the returned entity
func encryptSpotifyAuthHook(encryptor *crypto.Encryptor) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.SpotifyAuthFunc(func(ctx context.Context, m *ent.SpotifyAuthMutation) (ent.Value, error) {
			// Remember original tokens for decryption after save
			var originalAccessToken, originalRefreshToken string

			// Encrypt access_token if being set
			if accessToken, exists := m.AccessToken(); exists {
				originalAccessToken = accessToken
				// Check if already encrypted (for idempotency)
				if !crypto.IsEncrypted(accessToken) {
					encrypted, err := encryptor.Encrypt(accessToken)
					if err != nil {
						return nil, fmt.Errorf("failed to encrypt access_token: %w", err)
					}
					m.SetAccessToken(encrypted)
				}
			}

			// Encrypt refresh_token if being set
			if refreshToken, exists := m.RefreshToken(); exists {
				originalRefreshToken = refreshToken
				// Check if already encrypted (for idempotency)
				if !crypto.IsEncrypted(refreshToken) {
					encrypted, err := encryptor.Encrypt(refreshToken)
					if err != nil {
						return nil, fmt.Errorf("failed to encrypt refresh_token: %w", err)
					}
					m.SetRefreshToken(encrypted)
				}
			}

			// Execute the mutation
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return nil, err
			}

			// Decrypt the tokens in the returned entity
			if auth, ok := value.(*ent.SpotifyAuth); ok {
				if originalAccessToken != "" {
					auth.AccessToken = originalAccessToken
				}
				if originalRefreshToken != "" {
					auth.RefreshToken = originalRefreshToken
				}
			}

			return value, nil
		})
	}
}

// decryptSpotifyAuthInterceptor decrypts access_token and refresh_token fields after loading from database
func decryptSpotifyAuthInterceptor(client *ent.Client, encryptor *crypto.Encryptor) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			// Execute the query
			v, err := next.Query(ctx, q)
			if err != nil {
				return nil, err
			}

			// Decrypt tokens in the results
			switch result := v.(type) {
			case *ent.SpotifyAuth:
				if err := decryptSpotifyAuth(ctx, client, encryptor, result); err != nil {
					return nil, err
				}
			case []*ent.SpotifyAuth:
				for _, auth := range result {
					if err := decryptSpotifyAuth(ctx, client, encryptor, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptSpotifyAuth decrypts the access_token and refresh_token fields in a
// SpotifyAuth entity, self-healing legacy-format values to the marked
// ciphertext format.
func decryptSpotifyAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, auth *ent.SpotifyAuth) error {
	if auth == nil {
		return nil
	}

	accessPlain, accessHealed, err := resolveEncryptedField(encryptor, auth.AccessToken)
	if err != nil {
		return fmt.Errorf("failed to decrypt access_token for user %d: %w", auth.ID, err)
	}

	refreshPlain, refreshHealed, err := resolveEncryptedField(encryptor, auth.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to decrypt refresh_token for user %d: %w", auth.ID, err)
	}

	if accessHealed != "" || refreshHealed != "" {
		// Best-effort self-heal: persist the marked ciphertexts. The write
		// hook sees the marker and stores them as-is. Failures are ignored —
		// the read must never break, and the next read retries the heal.
		update := client.SpotifyAuth.UpdateOneID(auth.ID)
		if accessHealed != "" {
			update.SetAccessToken(accessHealed)
		}
		if refreshHealed != "" {
			update.SetRefreshToken(refreshHealed)
		}
		_ = update.Exec(ctx)
	}

	auth.AccessToken = accessPlain
	auth.RefreshToken = refreshPlain

	return nil
}

// encryptLastFMAuthHook encrypts the session_key field before saving to database
// and decrypts it in the returned entity
func encryptLastFMAuthHook(encryptor *crypto.Encryptor) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.LastFMAuthFunc(func(ctx context.Context, m *ent.LastFMAuthMutation) (ent.Value, error) {
			// Remember original session_key for decryption after save
			var originalSessionKey string

			// Encrypt session_key if being set
			if sessionKey, exists := m.SessionKey(); exists {
				originalSessionKey = sessionKey
				// Check if already encrypted (for idempotency)
				if !crypto.IsEncrypted(sessionKey) {
					encrypted, err := encryptor.Encrypt(sessionKey)
					if err != nil {
						return nil, fmt.Errorf("failed to encrypt session_key: %w", err)
					}
					m.SetSessionKey(encrypted)
				}
			}

			// Execute the mutation
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return nil, err
			}

			// Decrypt the session_key in the returned entity
			if auth, ok := value.(*ent.LastFMAuth); ok && originalSessionKey != "" {
				auth.SessionKey = originalSessionKey
			}

			return value, nil
		})
	}
}

// decryptLastFMAuthInterceptor decrypts session_key field after loading from database
func decryptLastFMAuthInterceptor(client *ent.Client, encryptor *crypto.Encryptor) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			// Execute the query
			v, err := next.Query(ctx, q)
			if err != nil {
				return nil, err
			}

			// Decrypt session_key in the results
			switch result := v.(type) {
			case *ent.LastFMAuth:
				if err := decryptLastFMAuth(ctx, client, encryptor, result); err != nil {
					return nil, err
				}
			case []*ent.LastFMAuth:
				for _, auth := range result {
					if err := decryptLastFMAuth(ctx, client, encryptor, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptLastFMAuth decrypts the session_key field in a LastFMAuth entity,
// self-healing legacy-format rows to the marked ciphertext format.
func decryptLastFMAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, auth *ent.LastFMAuth) error {
	if auth == nil || auth.SessionKey == "" {
		return nil
	}

	plaintext, healed, err := resolveEncryptedField(encryptor, auth.SessionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt session_key for user %d: %w", auth.ID, err)
	}
	if healed != "" {
		// Best-effort self-heal: persist the marked ciphertext. The write
		// hook sees the marker and stores it as-is. Failures are ignored —
		// the read must never break, and the next read retries the heal.
		_ = client.LastFMAuth.UpdateOneID(auth.ID).SetSessionKey(healed).Exec(ctx)
	}
	auth.SessionKey = plaintext

	return nil
}
