package database

import (
	"context"
	"fmt"
	"log/slog"

	"spotter/ent"
	"spotter/ent/hook"
	"spotter/ent/lastfmauth"
	"spotter/ent/listenbrainzauth"
	"spotter/ent/navidromeauth"
	"spotter/ent/spotifyauth"
	"spotter/internal/crypto"
)

// selfHealCtxKey marks a context as belonging to an interceptor self-heal
// write-back, whose value is ALREADY marked ciphertext and must be stored
// verbatim. Every other mutation carries application input and is ALWAYS
// encrypted — even input that happens to start with the enc:v1: marker.
// Deciding idempotency by value shape in the write hooks would re-open the
// bricking bug this package guards against: a credential that literally starts
// with "enc:v1:" would be stored as plaintext and then fail GCM authentication
// on every read (issue #335).
// Governing: ADR-0006 (AES-256-GCM at rest)
type selfHealCtxKey struct{}

// withSelfHeal returns a context that tells the write hooks to store the
// (already marked-ciphertext) value without re-encrypting it.
func withSelfHeal(ctx context.Context) context.Context {
	return context.WithValue(ctx, selfHealCtxKey{}, true)
}

// isSelfHeal reports whether ctx belongs to an interceptor self-heal write-back.
func isSelfHeal(ctx context.Context) bool {
	v, _ := ctx.Value(selfHealCtxKey{}).(bool)
	return v
}

// RegisterEncryptionHooks registers hooks to encrypt/decrypt sensitive data.
// Hook and interceptor failures are logged through logger (slog.Default when
// nil) in addition to being returned to the caller.
// Governing: ADR-0006 (AES-256-GCM application-layer encryption), ADR-0010 (slog structured logging)
func RegisterEncryptionHooks(client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}

	// Hook for encrypting password on NavidromeAuth create/update
	client.NavidromeAuth.Use(encryptPasswordHook(encryptor, logger))

	// Hook for decrypting password on NavidromeAuth query
	client.NavidromeAuth.Intercept(decryptPasswordInterceptor(client, encryptor, logger))

	// Hook for encrypting tokens on SpotifyAuth create/update
	client.SpotifyAuth.Use(encryptSpotifyAuthHook(encryptor, logger))

	// Hook for decrypting tokens on SpotifyAuth query
	client.SpotifyAuth.Intercept(decryptSpotifyAuthInterceptor(client, encryptor, logger))

	// Hook for encrypting session key on LastFMAuth create/update
	client.LastFMAuth.Use(encryptLastFMAuthHook(encryptor, logger))

	// Hook for decrypting session key on LastFMAuth query
	client.LastFMAuth.Intercept(decryptLastFMAuthInterceptor(client, encryptor, logger))

	// Hook for encrypting token on ListenBrainzAuth create/update
	// Governing: ADR-0006, SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-046)
	client.ListenBrainzAuth.Use(encryptListenBrainzAuthHook(encryptor, logger))

	// Hook for decrypting token on ListenBrainzAuth query
	client.ListenBrainzAuth.Intercept(decryptListenBrainzAuthInterceptor(client, encryptor, logger))
}

// resolveEncryptedField resolves a stored credential value on read and, when
// the stored form is legacy (pre-marker) ciphertext, returns a marked
// ciphertext to persist in its place (legacy -> enc:v1: migration).
//
// Resolution:
//  1. Marked (enc:v1:): decrypt with the key. A failure is a wrong key or
//     corrupted ciphertext; it is returned as an error and the row is NEVER
//     rewritten.
//  2. Unmarked and it decrypts: GCM authentication PROVES the value is legacy
//     ciphertext under the correct key, so the row is migrated — the plaintext
//     is returned and `migrated` holds the marked re-encryption to store.
//  3. Unmarked and it does NOT decrypt: either genuine plaintext from before
//     encryption was enabled, OR legacy ciphertext read under the WRONG key.
//     These two are indistinguishable, so the value is returned as-is with
//     migrated == "" and err == nil, and the stored row is left byte-for-byte
//     untouched.
//
// The invariant in case 3 is the whole point of this function (issue #335): a
// value that lacks the marker is NEVER re-encrypted on a decryption failure.
// Re-encrypting a wrong-key read would wrap the recoverable original under the
// wrong key + marker and permanently brick it once the correct key returns.
// Migration happens ONLY on a successful decryption, which is cryptographic
// proof the key is correct.
//
// Governing: ADR-0006 (AES-256-GCM at rest), ADR-0021 (encryption key rotation)
func resolveEncryptedField(encryptor *crypto.Encryptor, stored string) (plaintext, migrated string, err error) {
	if stored == "" {
		return "", "", nil
	}

	// Case 1: explicit marker. Must decrypt; a failure is real (wrong key or
	// corruption) and must surface — it is never healed away.
	if crypto.IsEncrypted(stored) {
		decrypted, err := encryptor.Decrypt(stored)
		if err != nil {
			return "", "", err
		}
		return decrypted, "", nil
	}

	// Case 2: unmarked but it decrypts -> legacy ciphertext under the correct
	// key. Migrate it to the marked format.
	if decrypted, decErr := encryptor.Decrypt(stored); decErr == nil {
		reencrypted, encErr := encryptor.Encrypt(decrypted)
		if encErr != nil {
			// Migration is best-effort; still expose the recovered plaintext
			// and leave the stored row untouched (migrated == "").
			return decrypted, "", nil
		}
		return decrypted, reencrypted, nil
	}

	// Case 3: unmarked and undecryptable -> genuine plaintext OR legacy
	// ciphertext under the WRONG key. Return as-is and DO NOT rewrite the
	// stored row: the original bytes must remain recoverable.
	return stored, "", nil
}

// encryptPasswordHook encrypts the password field before saving to database
// and decrypts it in the returned entity
func encryptPasswordHook(encryptor *crypto.Encryptor, logger *slog.Logger) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.NavidromeAuthFunc(func(ctx context.Context, m *ent.NavidromeAuthMutation) (ent.Value, error) {
			// Remember original password for decryption after save
			var originalPassword string
			if password, exists := m.Password(); exists {
				originalPassword = password
				// ALWAYS encrypt application input — even input that happens to
				// start with the enc:v1: marker (issue #335). Only the
				// interceptor self-heal path, which writes back values that are
				// already marked ciphertext, may skip encryption.
				// Governing: ADR-0006
				if !isSelfHeal(ctx) {
					// Encrypt the password
					encrypted, err := encryptor.Encrypt(password)
					if err != nil {
						logger.Error("encryption hook failed", "entity", "navidrome_auth", "field", "password", "error", err)
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
func decryptPasswordInterceptor(client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger) ent.Interceptor {
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
				if err := decryptNavidromeAuth(ctx, client, encryptor, logger, result); err != nil {
					return nil, err
				}
			case []*ent.NavidromeAuth:
				for _, auth := range result {
					if err := decryptNavidromeAuth(ctx, client, encryptor, logger, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptNavidromeAuth decrypts the password field in a NavidromeAuth entity,
// migrating legacy (pre-marker) ciphertext to the marked format on a successful
// decryption. A wrong-key read leaves the stored row untouched.
func decryptNavidromeAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger, auth *ent.NavidromeAuth) error {
	if auth == nil || auth.Password == "" {
		return nil
	}

	// Capture the stored value BEFORE mutating auth: it guards the migration
	// write-back as a compare-and-swap.
	stored := auth.Password

	// Governing: ADR-0006 — accept enc:v1:, legacy base64, and plaintext.
	// Legacy values that fail to decrypt are returned as plaintext instead of
	// erroring the whole query, and the stored row is never rewritten.
	plaintext, migrated, err := resolveEncryptedField(encryptor, stored)
	if err != nil {
		logger.Error("decryption hook failed", "entity", "navidrome_auth", "field", "password", "id", auth.ID, "error", err)
		return fmt.Errorf("failed to decrypt password for user %d: %w", auth.ID, err)
	}

	// Best-effort migration of legacy ciphertext to the marked format. The
	// Where guard makes it a compare-and-swap so a concurrent legitimate
	// credential update is never clobbered; the self-heal context tells the
	// write hook to store the already-marked value verbatim. Failures are
	// ignored — the read must never break; the next read retries.
	if migrated != "" {
		_, _ = client.NavidromeAuth.Update().
			Where(navidromeauth.ID(auth.ID), navidromeauth.Password(stored)).
			SetPassword(migrated).
			Save(withSelfHeal(ctx))
	}

	auth.Password = plaintext
	return nil
}

// encryptSpotifyAuthHook encrypts the access_token and refresh_token fields before saving to database
// and decrypts them in the returned entity
func encryptSpotifyAuthHook(encryptor *crypto.Encryptor, logger *slog.Logger) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.SpotifyAuthFunc(func(ctx context.Context, m *ent.SpotifyAuthMutation) (ent.Value, error) {
			// Remember original tokens for decryption after save
			var originalAccessToken, originalRefreshToken string

			// Encrypt access_token if being set. ALWAYS encrypt application
			// input — even input starting with the enc:v1: marker (issue #335);
			// only the interceptor self-heal path may skip encryption.
			// Governing: ADR-0006
			if accessToken, exists := m.AccessToken(); exists {
				originalAccessToken = accessToken
				if !isSelfHeal(ctx) {
					encrypted, err := encryptor.Encrypt(accessToken)
					if err != nil {
						logger.Error("encryption hook failed", "entity", "spotify_auth", "field", "access_token", "error", err)
						return nil, fmt.Errorf("failed to encrypt access_token: %w", err)
					}
					m.SetAccessToken(encrypted)
				}
			}

			// Encrypt refresh_token if being set (same self-heal rule as above)
			if refreshToken, exists := m.RefreshToken(); exists {
				originalRefreshToken = refreshToken
				if !isSelfHeal(ctx) {
					encrypted, err := encryptor.Encrypt(refreshToken)
					if err != nil {
						logger.Error("encryption hook failed", "entity", "spotify_auth", "field", "refresh_token", "error", err)
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
func decryptSpotifyAuthInterceptor(client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger) ent.Interceptor {
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
				if err := decryptSpotifyAuth(ctx, client, encryptor, logger, result); err != nil {
					return nil, err
				}
			case []*ent.SpotifyAuth:
				for _, auth := range result {
					if err := decryptSpotifyAuth(ctx, client, encryptor, logger, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptSpotifyAuth decrypts the access_token and refresh_token fields in a
// SpotifyAuth entity, migrating legacy-format values to the marked format on a
// successful decryption. A wrong-key read leaves the stored row untouched.
func decryptSpotifyAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger, auth *ent.SpotifyAuth) error {
	if auth == nil {
		return nil
	}

	// Governing: ADR-0006 — accept enc:v1:, legacy base64, and plaintext.
	// Legacy values that fail to decrypt are returned as plaintext instead of
	// erroring the whole query, and the stored row is never rewritten.
	if auth.AccessToken != "" {
		stored := auth.AccessToken
		plaintext, migrated, err := resolveEncryptedField(encryptor, stored)
		if err != nil {
			logger.Error("decryption hook failed", "entity", "spotify_auth", "field", "access_token", "id", auth.ID, "error", err)
			return fmt.Errorf("failed to decrypt access_token for user %d: %w", auth.ID, err)
		}
		if migrated != "" {
			_, _ = client.SpotifyAuth.Update().
				Where(spotifyauth.ID(auth.ID), spotifyauth.AccessToken(stored)).
				SetAccessToken(migrated).
				Save(withSelfHeal(ctx))
		}
		auth.AccessToken = plaintext
	}

	if auth.RefreshToken != "" {
		stored := auth.RefreshToken
		plaintext, migrated, err := resolveEncryptedField(encryptor, stored)
		if err != nil {
			logger.Error("decryption hook failed", "entity", "spotify_auth", "field", "refresh_token", "id", auth.ID, "error", err)
			return fmt.Errorf("failed to decrypt refresh_token for user %d: %w", auth.ID, err)
		}
		if migrated != "" {
			_, _ = client.SpotifyAuth.Update().
				Where(spotifyauth.ID(auth.ID), spotifyauth.RefreshToken(stored)).
				SetRefreshToken(migrated).
				Save(withSelfHeal(ctx))
		}
		auth.RefreshToken = plaintext
	}

	return nil
}

// encryptLastFMAuthHook encrypts the session_key field before saving to database
// and decrypts it in the returned entity
func encryptLastFMAuthHook(encryptor *crypto.Encryptor, logger *slog.Logger) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.LastFMAuthFunc(func(ctx context.Context, m *ent.LastFMAuthMutation) (ent.Value, error) {
			// Remember original session_key for decryption after save
			var originalSessionKey string

			// Encrypt session_key if being set. ALWAYS encrypt application input
			// — even input starting with the enc:v1: marker (issue #335); only
			// the interceptor self-heal path may skip encryption.
			// Governing: ADR-0006
			if sessionKey, exists := m.SessionKey(); exists {
				originalSessionKey = sessionKey
				if !isSelfHeal(ctx) {
					encrypted, err := encryptor.Encrypt(sessionKey)
					if err != nil {
						logger.Error("encryption hook failed", "entity", "lastfm_auth", "field", "session_key", "error", err)
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
func decryptLastFMAuthInterceptor(client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger) ent.Interceptor {
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
				if err := decryptLastFMAuth(ctx, client, encryptor, logger, result); err != nil {
					return nil, err
				}
			case []*ent.LastFMAuth:
				for _, auth := range result {
					if err := decryptLastFMAuth(ctx, client, encryptor, logger, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptLastFMAuth decrypts the session_key field in a LastFMAuth entity,
// migrating legacy (pre-marker) ciphertext to the marked format on a successful
// decryption. A wrong-key read leaves the stored row untouched.
func decryptLastFMAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger, auth *ent.LastFMAuth) error {
	if auth == nil || auth.SessionKey == "" {
		return nil
	}

	stored := auth.SessionKey

	// Governing: ADR-0006 — accept enc:v1:, legacy base64, and plaintext.
	// Legacy values that fail to decrypt are returned as plaintext instead of
	// erroring the whole query, and the stored row is never rewritten.
	plaintext, migrated, err := resolveEncryptedField(encryptor, stored)
	if err != nil {
		logger.Error("decryption hook failed", "entity", "lastfm_auth", "field", "session_key", "id", auth.ID, "error", err)
		return fmt.Errorf("failed to decrypt session_key for user %d: %w", auth.ID, err)
	}
	if migrated != "" {
		_, _ = client.LastFMAuth.Update().
			Where(lastfmauth.ID(auth.ID), lastfmauth.SessionKey(stored)).
			SetSessionKey(migrated).
			Save(withSelfHeal(ctx))
	}

	auth.SessionKey = plaintext
	return nil
}

// encryptListenBrainzAuthHook encrypts the token field before saving to database
// and decrypts it in the returned entity.
// Governing: ADR-0006 (AES-256-GCM at rest), SPEC music-provider-integration
// REQ "ListenBrainz Provider" (REQ-PROV-046)
func encryptListenBrainzAuthHook(encryptor *crypto.Encryptor, logger *slog.Logger) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return hook.ListenBrainzAuthFunc(func(ctx context.Context, m *ent.ListenBrainzAuthMutation) (ent.Value, error) {
			// Remember original token for decryption after save
			var originalToken string

			// Encrypt token if being set. ALWAYS encrypt application input —
			// even input starting with the enc:v1: marker (issue #335); only
			// the interceptor self-heal path may skip encryption.
			// Governing: ADR-0006
			if token, exists := m.Token(); exists {
				originalToken = token
				if !isSelfHeal(ctx) {
					encrypted, err := encryptor.Encrypt(token)
					if err != nil {
						logger.Error("encryption hook failed", "entity", "listenbrainz_auth", "field", "token", "error", err)
						return nil, fmt.Errorf("failed to encrypt token: %w", err)
					}
					m.SetToken(encrypted)
				}
			}

			// Execute the mutation
			value, err := next.Mutate(ctx, m)
			if err != nil {
				return nil, err
			}

			// Decrypt the token in the returned entity
			if auth, ok := value.(*ent.ListenBrainzAuth); ok && originalToken != "" {
				auth.Token = originalToken
			}

			return value, nil
		})
	}
}

// decryptListenBrainzAuthInterceptor decrypts token field after loading from database
func decryptListenBrainzAuthInterceptor(client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger) ent.Interceptor {
	return ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			// Execute the query
			v, err := next.Query(ctx, q)
			if err != nil {
				return nil, err
			}

			// Decrypt token in the results
			switch result := v.(type) {
			case *ent.ListenBrainzAuth:
				if err := decryptListenBrainzAuth(ctx, client, encryptor, logger, result); err != nil {
					return nil, err
				}
			case []*ent.ListenBrainzAuth:
				for _, auth := range result {
					if err := decryptListenBrainzAuth(ctx, client, encryptor, logger, auth); err != nil {
						return nil, err
					}
				}
			}

			return v, nil
		})
	})
}

// decryptListenBrainzAuth decrypts the token field in a ListenBrainzAuth entity,
// migrating legacy (pre-marker) ciphertext to the marked format on a successful
// decryption. A wrong-key read leaves the stored row untouched.
func decryptListenBrainzAuth(ctx context.Context, client *ent.Client, encryptor *crypto.Encryptor, logger *slog.Logger, auth *ent.ListenBrainzAuth) error {
	if auth == nil || auth.Token == "" {
		return nil
	}

	stored := auth.Token

	// Governing: ADR-0006 — accept enc:v1:, legacy base64, and plaintext.
	// Legacy values that fail to decrypt are returned as plaintext instead of
	// erroring the whole query, and the stored row is never rewritten.
	plaintext, migrated, err := resolveEncryptedField(encryptor, stored)
	if err != nil {
		logger.Error("decryption hook failed", "entity", "listenbrainz_auth", "field", "token", "id", auth.ID, "error", err)
		return fmt.Errorf("failed to decrypt token for user %d: %w", auth.ID, err)
	}
	if migrated != "" {
		_, _ = client.ListenBrainzAuth.Update().
			Where(listenbrainzauth.ID(auth.ID), listenbrainzauth.Token(stored)).
			SetToken(migrated).
			Save(withSelfHeal(ctx))
	}

	auth.Token = plaintext
	return nil
}
