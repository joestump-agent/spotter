---
status: accepted
date: 2026-03-02
decision-makers: Joe Stump
---

# ADR-0028: CSRF Protection Strategy — SameSite=Lax Cookie Attribute

## Context and Problem Statement

Spotter uses HTMX form submissions (POST) for login, preferences, playlist management, and other state-changing operations. These form submissions carry the `spotter_token` JWT session cookie for authentication. How should Spotter protect against Cross-Site Request Forgery (CSRF) attacks on these endpoints?

The session cookie is already set with `SameSite=Lax` (not `Strict`) because `SameSite=Strict` would prevent the browser from sending the cookie on OAuth callback redirects from Spotify and Last.fm, breaking provider linking flows (see issue #161).

## Decision Drivers

* `SameSite=Strict` is not viable — it breaks OAuth cross-site redirect chains (Spotify/Last.fm callback → Spotter authenticated route)
* Spotter is a single-user, self-hosted application — the attack surface for CSRF is limited to the operator's own browsing context
* OAuth callback handlers already validate a cryptographic `state` parameter for CSRF protection ([ADR-0022](./ADR-0022-threat-model-security-assumptions.md) T4)
* All POST endpoints require an authenticated session (JWT cookie) — unauthenticated POST endpoints do not exist except login
* The login form POSTs credentials to Navidrome; a CSRF attack on login would authenticate as the attacker's account, which is not useful in a single-user app
* Adding token-based CSRF middleware (double-submit cookie, Gorilla CSRF) would add complexity with minimal security benefit given the threat model ([ADR-0022](./ADR-0022-threat-model-security-assumptions.md))
* Modern browsers universally support `SameSite` (Chrome 80+, Firefox 69+, Safari 13+)

## Considered Options

* **Option A**: Document `SameSite=Lax` + `HttpOnly` + same-origin policy as sufficient CSRF protection
* **Option B**: Double-submit cookie CSRF token (custom middleware)
* **Option C**: Gorilla CSRF middleware (`gorilla/csrf`)

## Decision Outcome

Chosen option: **Option A** (SameSite=Lax is sufficient), because the combination of `SameSite=Lax`, `HttpOnly`, browser same-origin policy, and the single-user self-hosted deployment model provides adequate CSRF protection without additional middleware complexity.

`SameSite=Lax` prevents cross-origin sites from attaching the session cookie on POST requests. This is the exact attack vector that CSRF exploits. The browser will only send the cookie on cross-site navigations that are top-level GET requests (e.g., clicking a link), not on cross-site form submissions (POST), XHR/fetch, or iframe loads. Since all state-changing operations in Spotter use POST (or PUT/DELETE via HTMX), cross-site CSRF is blocked by the browser.

The only scenario where `SameSite=Lax` does not protect is a same-site attack (attacker-controlled subdomain). In Spotter's deployment model (single-user server, operator-controlled domain), this is not a realistic threat.

### Consequences

* Good, because no additional middleware, form tokens, or template changes are required
* Good, because no performance overhead from token generation, validation, or cookie management on every request
* Good, because OAuth redirect chains continue to work correctly (the reason `SameSite=Strict` was rejected)
* Good, because the existing `SecurityHeaders` middleware (`X-Frame-Options: DENY`, CSP) provides defense-in-depth against clickjacking-based CSRF variants
* Neutral, because `SameSite=Lax` allows the cookie on top-level cross-site GET navigations — but GET handlers do not perform state-changing operations
* Bad, because if a future change introduces state-changing GET endpoints, CSRF protection would be incomplete — this MUST NOT happen (all mutations MUST use POST/PUT/DELETE)

### Confirmation

Compliance is confirmed when:
- Session cookies are set with `SameSite=Lax` in both login and logout flows (`internal/handlers/auth.go`)
- OAuth state cookies are set with `SameSite=Lax` (`internal/handlers/spotify_auth.go`, `internal/handlers/lastfm_auth.go`)
- No state-changing operation uses GET method
- `SecurityHeaders` middleware is applied globally (X-Frame-Options, CSP)
- Tests verify `SameSite=Lax` is set on session cookies

## Pros and Cons of the Options

### Option A — SameSite=Lax as sufficient protection

Rely on the browser's `SameSite=Lax` cookie attribute to prevent cross-site POST requests from attaching the session cookie. No server-side CSRF token infrastructure.

* Good, because zero implementation complexity — already in place
* Good, because no template changes needed (no hidden CSRF token fields in forms)
* Good, because no middleware overhead on every request
* Good, because compatible with HTMX's `hx-post` / `hx-swap` patterns without special header injection
* Neutral, because requires discipline to never add state-changing GET endpoints
* Bad, because does not protect against same-site (subdomain) attacks — acceptable given deployment model

### Option B — Double-submit cookie CSRF token

Generate a random CSRF token, set it as a cookie, and require it as a hidden form field or custom header on every POST. The server validates that the cookie value matches the submitted value.

* Good, because provides defense-in-depth beyond SameSite
* Good, because well-understood CSRF mitigation pattern
* Bad, because requires injecting a hidden field into every Templ form template (30+ forms)
* Bad, because requires custom middleware to generate, validate, and rotate tokens
* Bad, because HTMX `hx-post` requests would need a custom header or `hx-vals` injection for the token
* Bad, because adds cookie management complexity (token rotation, expiry, race conditions with concurrent tabs)

### Option C — Gorilla CSRF middleware (`gorilla/csrf`)

Use the `gorilla/csrf` package to add per-request CSRF tokens with automatic form field injection.

* Good, because battle-tested library with proven security
* Good, because handles token generation, rotation, and validation automatically
* Bad, because adds an external dependency for a single-user app with existing browser-level protection
* Bad, because requires template integration to inject `csrf.TemplateField` into every form
* Bad, because Gorilla CSRF uses `SameSite=Strict` by default for its own cookie, which would conflict with OAuth flows
* Bad, because HTMX integration requires custom JavaScript to extract the token from meta tags or cookies

## More Information

* **Session cookie setup**: `internal/handlers/auth.go:142-153` — `HttpOnly: true`, `Secure: config`, `SameSite: Lax`
* **OAuth state CSRF**: `internal/handlers/spotify_auth.go:22-50` — cryptographic state parameter validated on callback
* **Security headers**: `internal/middleware/security.go` — `X-Frame-Options: DENY`, CSP, `X-Content-Type-Options: nosniff`
* **Threat model**: [ADR-0022](./ADR-0022-threat-model-security-assumptions.md) — T3 (session cookie theft), T4 (CSRF on OAuth callbacks), T5 (input validation)
* **SameSite=Lax constraint**: Issue #161 — OAuth redirect chains require Lax, not Strict
* **Related**: [ADR-0005](./ADR-0005-navidrome-primary-identity-provider.md) (Navidrome primary identity — JWT cookie approach), [ADR-0022](./ADR-0022-threat-model-security-assumptions.md) (threat model)
