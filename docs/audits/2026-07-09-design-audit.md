# Design Audit Report — spotter — 2026-07-09

Scope: Full project
Analyzed: 29 ADRs (28 accepted, 1 superseded), 17 specs (15 accepted, 2 proposed), ~310 spec requirements, ~115 source files (internal/ + cmd/ + views, excluding generated ent/templ output)
Total findings: **110** (50 critical, 56 warning, 4 info)

> Method note: the SDD plugin v5 mandates qmd hybrid retrieval for per-file artifact matching. qmd is not installed in this remote container (its ~2GB model cache is impractical here), so this audit used the pre-v5 full corpus scan: every spec/ADR read in full and verified against code by six parallel domain auditors plus a meta-auditor, with adversarial self-checks and file:line evidence for every finding. `go build`, `go vet`, and the full test suite (27 packages) pass clean.

---

## Code vs. Specification Drift

### Critical (MUST/SHALL violations) — 40

| # | Finding | Spec | Location |
|---|---------|------|----------|
| 1 | `rotate-key` prints the new encryption key to stdout; REQ-ROT-051 forbids logging key values | key-rotation REQ-ROT-051 | cmd/admin/main.go:182 |
| 2 | AuthMiddleware returns 401(+HX-Redirect) for HTMX/SSE instead of unconditional 303 (design.md documents 401 — spec stale) | user-authentication REQ-MIDDLEWARE-002 | cmd/server/main.go:677-690 |
| 3 | OAuth state cookie Secure flag from config, not `r.TLS != nil` (code is safer; spec text wrong for reverse-proxy TLS per ADR-0022) | user-authentication REQ-SPOTIFY-005, REQ-LASTFM-003 | internal/handlers/spotify_auth.go:76; lastfm_auth.go:66 |
| 4 | `include_unmatched_tracks` placeholder mode unimplemented (config field unused) | playlist-sync-navidrome REQ-PLSYNC-020 | internal/services/playlist_sync.go:196-211; config.go:19-20 |
| 5 | `min_match_confidence` default is 0.8 in code vs 0.7 in spec + ADR-0014 (deliberate per code comment) | playlist-sync-navidrome REQ-PLSYNC-003; ADR-0014 | internal/config/config.go:316 |
| 6 | No-listens fallback uses epoch instead of configurable `sync.history_lookback` (720h); key doesn't exist | listen-playlist-sync REQ-SYNC-020 | internal/services/sync.go:268-272 |
| 7 | History sync stores denormalized strings only; entity creation/linking deferred to metadata ticker (out-of-scope subsystem, up to 1h later) | listen-playlist-sync REQ-SYNC-021 | internal/services/sync.go:550-562 |
| 8 | Playlists removed at the provider are never deactivated/deleted locally | listen-playlist-sync REQ-SYNC-032 | internal/services/sync.go:635-758 |
| 9 | Normalization cannot strip `(feat. ...)` (fixed-suffix TrimSuffix only) | playlist-sync-navidrome REQ-PLSYNC-005 | internal/services/track_matcher.go:311-349 |
| 10 | Spotify uses plain auth-code flow with client secret; spec mandates PKCE | music-provider-integration REQ-PROV-031 | internal/providers/spotify/spotify.go:76-98 |
| 11 | Spotify `CreatePlaylist` is a TODO stub returning nil | music-provider-integration REQ-PROV-003/030 | spotify.go:530-535 |
| 12 | Spotify `GetRecentListens` doesn't paginate to `since` (single 50-item call) | music-provider-integration REQ-PROV-033 | spotify.go:246-268 |
| 13 | `Track.ISRC` never populated by any provider | music-provider-integration REQ-PROV-022 | spotify.go:295-311,440 |
| 14 | Navidrome implements `Authenticator` (spec says MUST NOT; design.md says it should — spec/design conflict) | music-provider-integration REQ-PROV-052 | internal/providers/navidrome/navidrome.go:36-41 |
| 15 | `Registry.Register` silently overwrites duplicate registrations (must error) | metadata-enrichment-pipeline REQ-ENRICH-050 | internal/enrichers/enrichers.go:220-222 |
| 16 | Registry lacks `ArtistEnrichers()/AlbumEnrichers()/TrackEnrichers()/IDMatchers()` (ad-hoc type assertions instead) | metadata-enrichment-pipeline REQ-ENRICH-051 | enrichers.go:208-237 |
| 17 | Overlapping enrichment runs not skipped — `syncMetadataForUsers` spawns and returns; governing comment falsely claims it blocks | metadata-enrichment-pipeline REQ-ENRICH-041 | cmd/server/main.go:283-340 |
| 18 | Later enrichers overwrite earlier non-empty fields (guards check stale entity value, not accumulated update) | metadata-enrichment-pipeline REQ-ENRICH-020 | internal/services/metadata.go:678-680 |
| 19 | `*Data` structs lack `[]ImageData` (images via separate methods; design documents code shape) | metadata-enrichment-pipeline REQ-ENRICH-021 | enrichers.go:32-120 |
| 20 | No best-image selection (IsPrimary → Likes → dimensions) anywhere | metadata-enrichment-pipeline REQ-ENRICH-022 | metadata.go:760-828; handlers/images.go:60-82 |
| 21 | Image max dimension hardcoded 1024; `metadata.images.max_width` config unused; `downloadFile` does raw io.Copy with no resize/PNG | metadata-enrichment-pipeline REQ-ENRICH-031 | enrichers/images.go:22-24; metadata.go:1683-1707 |
| 22 | SyncEvent lacks status/entity-ID; enricher failures only slog-logged (no error SyncEvents) | metadata-enrichment-pipeline REQ-ENRICH-043 | ent/schema/syncevent.go:20-64 |
| 23 | Tag names stored raw/untrimmed (SHALL store trimmed original casing) | unified-tag-taxonomy "Tag Normalization" | internal/tags/upsert.go:51 |
| 24 | entity_tags INSERT on separate raw *sql.DB, not same transaction as Ent edge; no dissociation sync path | unified-tag-taxonomy "Denormalized Entity Tags Table" | upsert.go:63-96 |
| 25 | `BackfillTags` migration never invoked from any binary | unified-tag-taxonomy "Data Migration" | internal/migrations/backfill_tags.go:28 |
| 26 | UI tag rendering ignores `TypedTagBadge`/badge-accent spec; legacy fields with TODO(SPEC-0014) referencing unmerged PR #255 | unified-tag-taxonomy "UI Tag Visual Differentiation" | views/artists/show.templ:261-275 et al. |
| 27 | Hardcoded fallback prompts despite "Hardcoded prompt strings are NOT permitted" (design.md chose the fallback — spec/design conflict) | vibes-ai-mixtape-engine REQ-VIBES-013 | internal/vibes/generator.go:567-600; enhancer.go:460-492 |
| 28 | Track count bounded by max only; `vibes.min_tracks` never referenced | vibes-ai-mixtape-engine REQ-VIBES-012 | generator.go:136-146 |
| 29 | WaitGroup tracks ticker-loop goroutines (REQ-WG-004 says per-user only) | graceful-shutdown REQ-WG-004 | cmd/server/main.go:217,278,355,413 |
| 30 | Shutdown order/budget wrong: srv.Shutdown first with full 30s, then wg.Wait same ctx (spec: wg≤25s → HTTP≤5s → DB) | graceful-shutdown REQ-TMO-003 | main.go:638-659 |
| 31 | `metric.background_tick` fires after spawning, not after processing; error counters are racy and sync errors always 0 | observability REQ-BG-001 | main.go:251-259,307-315,396-404 |
| 32 | `metric.sync` per-provider events carry whole-sync aggregates | observability REQ-BG-003 | internal/services/sync.go:74-105 |
| 33 | `metric.enricher` emitted per entity-type pass, not per enricher | observability REQ-BG-004 | metadata.go:611-617,897-903,1316-1322 |
| 34 | OpenAI enricher LLM calls emit no `metric.llm` | observability REQ-LLM-001 | enrichers/openai/openai.go:240-295 |
| 35 | `ClearFatal()` has zero call sites — fatal-blocked provider stays blocked after user reconnects, until restart | error-handling REQ-STATE-004 | services/resilience.go:314-325 |
| 36 | Backoff jitter applied after 30m cap → up to 37.5m (test asserts it); spec REQ-BACK-002 forbids >30m | error-handling REQ-BACK-002 | resilience.go:213-221 |
| 37 | Unparseable responses classified retriable, not fatal — contract-change errors retry forever | error-handling REQ-ERR-003 | resilience.go:90-100 |
| 38 | No `RecentListenPayload` type or `PublishRecentListen` method; raw `*ent.Listen` in hand-built Event{} | event-bus-sse REQ-BUS-011/012 | events/bus.go; sync.go:578-581 |
| 39 | README claims pg/mysql drivers require CGO — exact opposite of spec (they're pure Go; SQLite needs CGO) | multi-database-support "End-User Documentation" | README.md:57 |
| 40 | Sync-failure emails link to Navidrome's base URL as "Spotter preferences page"; no Spotter base URL exists in config | SPEC-0015 "Email Content" | cmd/server/main.go:131; notifications/templates.go:62 |

### Warning (SHOULD violations / scenario gaps) — 27

| # | Finding | Spec | Location |
|---|---------|------|----------|
| 41 | Existing user without NavidromeAuth record never gets one on login (update-only branch) — background sync left without credentials | user-authentication REQ-AUTH-007 | internal/handlers/auth.go:108-117 |
| 42 | Listen dedup key uses name+artist instead of mandated provider track ID (not stored at all) | listen-playlist-sync REQ-SYNC-021 | sync.go:526-534 |
| 43 | Only matched_track_count persisted (unmatched count, match rate not stored) | playlist-sync-navidrome REQ-PLSYNC-021 | playlist_sync.go:274-292 |
| 44 | Completion SyncEvent omits match rate and final status | playlist-sync-navidrome REQ-PLSYNC-060 | playlist_sync.go:303-313 |
| 45 | "(2011 Remaster)" unstrippable — Scenario 3 unachievable via exact tier | playlist-sync-navidrome Scenario 3 | track_matcher.go:347-349 |
| 46 | Promised weekly bulk similar-artists enrichment: `FindSimilarArtistsForAll` has no production caller | similar-artists-discovery Overview | similar_artists.go:433-485 |
| 47 | No `EnrichAll()`; per-user SyncAll + main.go loop (shape drift) | metadata-enrichment-pipeline REQ-ENRICH-040/041 | metadata.go:98 |
| 48 | Refreshed Spotify tokens never persisted; rotated refresh token dropped → syncs fail until reconnect | music-provider-integration Scenario 2 | providers/spotify/spotify.go:179-186 |
| 49 | Tag relationships directional in Ent schema; spec says undirected (feature unused) | unified-tag-taxonomy "Tag Relationships" | ent/schema/tag.go:52-54 |
| 50 | DJ deletion neither cascades nor confirm-deletes (UI blocks; direct DELETE = FK 500) | vibes REQ-VIBES-003 | handlers/vibes.go:210-236 |
| 51 | Enhancer candidate tracks not user-scoped — other users' tracks can enter playlists (multi-user) | vibes REQ-VIBES-022 | vibes/enhancer.go:414-421 |
| 52 | Mixtape stores track_ids array; spec/design claim PlaylistTracks edge | vibes Data Model | ent/schema/mixtape.go:56-58 |
| 53 | `vibes.max_tokens` default 4000 vs spec 4096 | vibes Config Reference | config.go:327 |
| 54 | `vibes.prompts_directory` default differs from spec (spec is the outlier vs ADR-0008) | vibes Config Reference | config.go:226-234,329 |
| 55 | Mixtape scheduling in scope but schedule field never consumed — no regeneration scheduler exists | vibes Scope | ent/schema/mixtape.go:39-42 |
| 56 | Backoff ladder starts one step late (first delay 60s, spec ~30s) | error-handling Scenarios 1-2 | resilience.go:244-246 |
| 57 | No refresh-and-retry-once on 401 (proactive refresh only; 401 = fatal immediately) | error-handling REQ-ERR-002 Scenario 4 | sync.go:295-320 |
| 58 | Loops don't log "loop shutting down" on ctx.Done | graceful-shutdown REQ-CTX-001 | main.go:224-226,334-336,369-371 |
| 59 | No still-running goroutine count logged at drain timeout | graceful-shutdown REQ-TMO-004 | main.go:653-659 |
| 60 | sync.go builds Event{} literals in 5 places despite PublishNotification | event-bus-sse REQ-BUS-012 | sync.go:238,334,384,444,911 |
| 61 | Fuzzy match above threshold with nil NavidromeID emits no metric.track_match | observability REQ-MATCH-003 | track_matcher.go:194-219 |
| 62 | lib/pq + go-sql-driver flagged `// indirect` (spec requires direct) | multi-database-support "go.mod Dependencies" | go.mod:26,32 |
| 63 | Shipped compose examples omit required SPOTTER_LIDARR_* env — crash-loop at config load | multi-database-support scenarios | docker-compose.{postgres,mariadb}.yml |
| 64 | Email subject/body use raw provider keys ("spotify") vs spec display names + subject format | SPEC-0015 "Email Content" | notifications/templates.go:56 |
| 65 | SMTP-disabled skip not logged at debug (NoopNotifier silent) | SPEC-0015 "SMTP Configuration" | notifications/notifier.go:30-32 |
| 66 | Album with valid MBID never enqueued to Lidarr if artist lacks MBID / edge unloaded (extra preconditions) | SPEC-0017 "Enricher Decoupling" | enrichers/lidarr/lidarr.go:213 |
| 67 | Permanent-error carve-out (attempts→10, no retry_at) exists nowhere in spec/ADR-0029 | SPEC-0017 "Backoff Strategy" | lidarr_submitter.go:225-249,586-593 |

## Code vs. ADR Drift

| Severity | Finding | ADR | Location |
|----------|---------|-----|----------|
| [CRITICAL] | ADR-0024 (accepted, tag browsing UI) entirely unimplemented: no handlers, templates, routes, sidebar entry, or JSONB queries | ADR-0024 | cmd/server/main.go:544-569 |
| [WARNING] | Logout is a state-changing GET (`/logout`, `/auth/logout`) — ADR-0028 forbids state-changing GETs (cross-site logout CSRF under SameSite=Lax) | ADR-0028 | main.go:454-455; handlers/auth.go:167-183 |
| [WARNING] | `MetadataService.downloadFile` bypasses `enrichers.DownloadAndSaveImage()` (no resize/PNG) | ADR-0027 | metadata.go:1683-1707 |
| [WARNING] | Raw `os.Getenv` for SPOTTER_SHUTDOWN_TIMEOUT, SPOTTER_MAX_CONCURRENT_JOBS, admin DATABASE_DRIVER/SOURCE bypasses Viper | ADR-0009 | main.go:187,196; cmd/admin/main.go:53,57 |
| [WARNING] | input.css contains ~23 lines beyond the three Tailwind directives | ADR-0011 | static/css/input.css:5-26 |

## ADR vs. Spec Inconsistencies (incl. spec↔design contradictions)

| Severity | Finding | Artifact A | Artifact B |
|----------|---------|-----------|-----------|
| [CRITICAL] | REQ-ROT-050 summary template prints the new key; REQ-ROT-051 forbids it (design.md sides with 051) | key-rotation REQ-ROT-050 | REQ-ROT-051 |
| [CRITICAL] | REQ-MIDDLEWARE-003 (lookup by username) contradicts REQ-SESSION-001 + design.md (JWT, lookup by UserID) | user-auth REQ-MIDDLEWARE-003 | REQ-SESSION-001/design.md |
| [CRITICAL] | REQ-PROV-052 (no Authenticator) vs design.md decision (implement with SupportsAuth()=false) | music-provider spec.md:110 | design.md:73-87 |
| [CRITICAL] | REQ-ENRICH-001 Priority() vs ADR-0015/design config-driven order (code follows ADR) | metadata spec | ADR-0015 |
| [CRITICAL] | REQ-ENRICH-005 matcher signature/phase vs design.md + code | metadata spec | design.md |
| [CRITICAL] | REQ-VIBES-013 (no hardcoded prompts) vs design.md (chose hardcoded fallback) | vibes spec.md:61-62 | design.md:63-64,335-339 |
| [CRITICAL] | graceful-shutdown design.md endorses Shutdown-before-wg.Wait + loop tracking against spec MUSTs | graceful-shutdown design.md | spec REQ-TMO-003/WG-004 |
| [CRITICAL] | REQ-BACK-001 formula permits 37.5m; REQ-BACK-002 forbids >30m (code follows formula) | error-handling REQ-BACK-001 | REQ-BACK-002 |
| [WARNING] | user-auth spec says PostgreSQL; ADR-0006/0022 say SQLite; multi-DB reality is driver-selectable | user-auth spec | ADR-0006/0022 |
| [WARNING] | REQ-PLSYNC-005 mandates feat-stripping; REQ-TM-041 omits it for the same function | playlist-sync spec | track-matching spec |
| [WARNING] | ADR-0024 (JSONB, rejects Tag entity) vs ADR-0025/SPEC-0014 (Tag entity) — no supersession marker | ADR-0024 | ADR-0025 |
| [WARNING] | Spec config keys metadata.enricher_order / metadata.fanart_api_key vs actual metadata.order / metadata.fanart.api_key | metadata spec | ADR-0015/code |
| [WARNING] | event-bus design.md documents recent-listen payload as full Listen entity vs spec RecentListenPayload | event-bus design.md | spec REQ-BUS-011 |
| [WARNING] | ADR-0026 diagram burns cooldown before send; SPEC-0015 requires no record on send failure (code follows spec) | ADR-0026:131-139 | SPEC-0015 |
| [WARNING] | Lidarr design.md says cap 20 / 30s; spec+ADR-0029+config say 50 / 3m | SPEC-0017 design.md | spec.md/ADR-0029 |

## Coverage Gaps

| Severity | Area | Description |
|----------|------|-------------|
| [INFO] | internal/types/ | Shared background Task type ungoverned (adjacent to ADR-0013) |
| [INFO] | internal/version/ | Build-time version injection ungoverned |
| [INFO] | internal/auth/, internal/crypto/ | Governed but zero `Governing:` comments — untraceable by /sdd:check |

## Stale Artifacts

| Severity | Artifact | Issue |
|----------|----------|-------|
| [WARNING] | SPEC-0014 ID collision | unified-tag-taxonomy AND multi-database-support both claim SPEC-0014; ~51 `Governing: SPEC-0014` comments ambiguous. Renumber one (SPEC-0016 appears unused... note: SPEC-0016 is referenced by the SDD plugin's own docs; verify before reuse) |
| [WARNING] | lidarr-submission-queue spec | Status `proposed` but fully implemented (44 Governing refs) — promote |
| [WARNING] | unified-tag-taxonomy spec | Status `proposed` but ~70% implemented (51 Governing refs) — promote or track completion |
| [WARNING] | AGENTS.md:284 | Claims SQLite-only — stale vs ADR-0023 |
| [WARNING] | ADR-0022 T8 | Claims no rate limiting; per-IP login rate limiting exists |
| [WARNING] | user-auth/ADR-0005/0006/0021/0022 | Stale file/line citations (AuthMiddleware, route groups, encryptor.go filename, config lines) |
| [WARNING] | user-auth spec | Governing comments cite REQ names that don't exist in the spec (timeout, sanitization, security headers, secure-cookie) |
| [WARNING] | similar-artists spec REQ-SIM-020/021/023 | Describe raw HTTP client; code uses shared llm.Client (design.md already updated) |
| [WARNING] | ADR-0014 | Names consumer files mixtape_generator.go/playlist_enhancer.go (actual generator.go/enhancer.go) |
| [WARNING] | 3 sync design.md files | Say PostgreSQL while ADR-0014/specs say SQLite |
| [WARNING] | playlist-sync design.md | Claims 202 responses; handlers return 200 + HTMX partial |
| [WARNING] | listen/similar-artists specs | Stale line citations (scheduler, handler, service init) |
| [WARNING] | ADR-0008 | Confirmation describes pre-llm.Client refactor reality |
| [WARNING] | error-handling design.md | Migration step 7 claims ClearFatal wired into reconnect handlers — false |
| [WARNING] | observability design.md | Claims metric.llm emitted from llm/client.go + openai.go — false |
| [WARNING] | SPEC-0015 Implementation Notes | Points at nonexistent files (services/notification.go, services/backoff_manager.go) |
| [WARNING] | SPEC-0017 | Status `proposed` but fully implemented (dup of above lidarr row — same artifact, kept once in counts) |
| [WARNING] | ADR-0015 | Cites wrong default enricher order and stale registration lines |
| [WARNING] | metadata spec Enricher Inventory | Priority column matches nothing; OpenAI also implements TrackEnricher |

## Policy Violations

| Severity | Finding | Source | Location |
|----------|---------|--------|----------|
| [CRITICAL] | Retry cap: "SHOULD cap at 10" immediately followed by "after 10 failed attempts MUST NOT retry" — contradictory obligation levels | lidarr-submission-queue | spec.md:183-185 |
| [INFO] | REQ-SPOTIFY-008 "appropriate error URL" undefined — untestable | user-authentication | spec.md:121 |

---

## Summary

| Category | Critical | Warning | Info | Total |
|----------|----------|---------|------|-------|
| Code vs. Spec | 40 | 27 | 0 | 67 |
| Code vs. ADR | 1 | 4 | 0 | 5 |
| ADR vs. Spec | 8 | 7 | 0 | 15 |
| Coverage Gaps | 0 | 0 | 3 | 3 |
| Stale Artifacts | 0 | 18 | 0 | 18 |
| Policy Violations | 1 | 0 | 1 | 2 |
| **Total** | **50** | **56** | **4** | **110** |

## Recommended Actions

1. [CRITICAL] Fix code-side MUST violations with user impact: stop printing the rotation key (REQ-ROT-051); wire `ClearFatal()` into reconnect handlers (REQ-STATE-004); guard overlapping enrichment runs (REQ-ENRICH-041); fix enricher field-overwrite ordering (REQ-ENRICH-020); fix Spotter base URL in notification emails (SPEC-0015); fix backoff cap (REQ-BACK-002).
2. [CRITICAL] Resolve the SPEC-0014 ID collision and mark ADR-0024 superseded by ADR-0025 (or implement it) — run `/sdd:status`.
3. [CRITICAL] Reconcile spec↔design contradictions where code already follows design.md (user-auth middleware/cookies, Navidrome Authenticator, enricher Priority/matcher shape, vibes fallback prompt, shutdown ordering, backoff formula): amend spec.md or file code changes per the source-of-truth decision.
4. [CRITICAL] Finish or descope: Spotify CreatePlaylist stub, ISRC population, pagination-to-since, PKCE (amend spec if client-secret flow is the accepted design for self-hosted).
5. [CRITICAL] Complete SPEC-0014 taxonomy: trimmed names, transactional entity_tags, wire BackfillTags, land the UI (PR #255).
6. [WARNING] Promote implemented `proposed` specs via `/sdd:status` (lidarr-submission-queue, unified-tag-taxonomy after completion).
7. [WARNING] Fix observability metric semantics (background_tick, per-provider sync, per-enricher, metric.llm in OpenAI enricher).
8. [INFO] Add `Governing:` comments to internal/auth and internal/crypto during the next feature PRs touching them (no retroactive-comment PRs per ADR-0020); consider `/sdd:adr` for internal/types and internal/version if they grow.

---

# Appendix: Proactive Bug Review (separate from design audit)

Independent three-agent correctness review (web layer; services/concurrency; integrations/infra). Security calibrated for self-hosted: outright bugs only, no hardening audit. Overlaps with audit findings noted.

## High
| Bug | Location |
|-----|----------|
| No scheduler overlap guard + Track table lacks unique (artist,name) index → concurrent SyncAll creates duplicate tracks; subsequent `.Only()` queries fail permanently ("not singular") | cmd/server/main.go:283-341; metadata.go:459-493; ent/schema/track.go:139-142 |
| MySQL/MariaDB support broken at startup: entity_tags DDL is SQLite syntax; tag upsert uses $N + ON CONFLICT (not MySQL) | database/entity_tags.go:43-57; tags/upsert.go:95-104 |
| Image downloads use http.Get with no timeout/ctx — a stalled host hangs a user's enrichment run forever | enrichers/images.go:48 |

## Medium
| Bug | Location |
|-----|----------|
| Data race on tick error counters; metric always reports errors=0 | main.go:235-259,291-315,380-404 |
| LidarrSubmitter hot-loops resubmitting the same item if status UPDATE fails persistently | lidarr_submitter.go:135-285 |
| Interrupted image download leaves truncated file that later runs adopt as valid | metadata.go:1683-1707,1597-1604 |
| Artist.musicbrainz_id/spotify_id globally unique → multi-user enrichment permanently fails for shared artists + endless retry (LastEnrichedAt lost) | ent/schema/artist.go:36-44; metadata.go:656-663 |
| persistPlaylistTracks collapses duplicate tracks / shadows same-key rows → silently drops occurrences, propagates to Navidrome | sync.go:786-893 |
| Failed login shows nothing under HTMX (401 body not swapped by htmx 1.9) | handlers/auth.go:47-51 |
| Mixtape/enhancement/similar-artist SSE toasts never rendered (no sse-swap listeners for those event names) | handlers/sse.go:69-178; toast.templ:78 |
| applyEnhancementToNavidrome delete-then-insert with no transaction → truncated playlist synced to Navidrome on failure (data loss) | handlers/playlists.go:1097-1160 |
| Weekly chart data keys never match axis labels → "6m" chart renders all zeros | handlers/artists.go:384-417 |
| SSE stream killed every 60s by global Timeout middleware; events lost in reconnect gaps | main.go:435; sse.go:40-43 |
| Spotify refreshed tokens never persisted (rotated refresh token lost → syncs fail until reconnect) | providers/spotify/spotify.go:162-187 |
| IsEncrypted base64 false-positive stores secret plaintext, then every read of that row errors with no self-heal | crypto/encrypt.go:103-117 |
| rotate-key UPDATEs while rows cursor open — fails on Postgres/MySQL ("conn busy") | cmd/admin/main.go:263-320 |
| UpdatePlaylistTracks swallows current-track fetch errors → duplicates old+new tracks in Navidrome, reports success | providers/navidrome/navidrome.go:850,482-543 |
| MusicBrainz queries lack Lucene escaping → names with special chars 400 forever | enrichers/musicbrainz/musicbrainz.go:189,243,299-306 |
| Enhancer candidate tracks not user-scoped (cross-user leakage; dup of audit REQ-VIBES-022) | vibes/enhancer.go:414-421 |

## Low
NavidromeAuth never recreated on login (dup REQ-AUTH-007); syncErrors race (dup); CSP blocks cdnjs timeago (all relative timestamps dead); mixtape seeds not ownership-scoped (cross-user IDOR read); playlists index swallows managed-IDs query error; Syncer.Sync always returns nil; image filename collisions per type; HasCoverArt set after prompt render; portrait images upscaled + truncated-file adoption; Last.fm cb param unencoded; Navidrome static-salt token persisted in image URLs/logs (password-equivalent; violates REQ-PROV-051); rotate-key BEGIN EXCLUSIVE on pooled handle.

## Verified clean (both passes)
AES-GCM implementation; JWT validation; OAuth CSRF state handling; event bus locking; SSE framing; ownership scoping across handlers (except noted); pagination math; response-body hygiene; Spotify 429 handling; Lidarr retry bounds; graceful-shutdown building blocks; `go build`, `go vet`, all 27 test packages pass.
