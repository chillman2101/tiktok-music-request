# Multi-Tenant Song Request — Design

Status: approved, ready for implementation planning.

## Goal

Turn the TikTok Live song-request app from single-tenant (one backend =
one streamer, configured via env vars) into multi-tenant, so it can be
sold to multiple streamers who share one Railway deployment on the free
tier. The owner manually onboards each streamer via a super-admin panel.

## Non-goals

- Self-service signup (owner generates tenants manually for now).
- Billing/payment integration.
- Horizontal scaling / multiple backend instances (still a single
  process, same as today).
- Migrating any existing single-tenant state — this is a clean break.
  The old env vars (`BROADCASTER_USERNAME`, `TIKTOK_USERNAME`,
  `OVERLAY_TOKEN`, `ADMIN_TOKEN`, single `config.json`) are removed
  entirely, not kept as a fallback.

## Architecture

One backend Go process, one `tiktok-connector` Node process — same
deployment shape as today. The backend now hosts many tenants at once,
each with its own `Queue`, `PendingQueue`, `Player`, `Hub`, and `Config`,
held in a `map[TenantID]*Tenant` in memory.

A tenant **registry** (list of tenants, their tokens, their TikTok
username) is persisted to `tenants.json` on a **Railway volume**, so it
survives redeploys — unlike each tenant's queue/pending state, which is
allowed to reset on restart (same tradeoff the single-tenant version
already had, just now per-tenant instead of global).

## Token model (4 kinds)

1. **`SUPER_ADMIN_TOKEN`** (env var, the only one that stays a static
   env var) — protects the super-admin panel where the owner creates
   and deletes tenants.
2. **`OverlayToken`** (per tenant, random, stored in `tenants.json`) —
   given to the streamer for their OBS overlay URL.
3. **`AdminToken`** (per tenant, random, stored in `tenants.json`) —
   given to the streamer for their own admin CMS (approve requests,
   change auto-approve, manage queue — scoped to just their tenant).
4. **`BACKEND_SHARED_SECRET`** (env var, stays singular) — authenticates
   `tiktok-connector` to the backend in general. Not per-tenant, because
   one connector process serves every tenant's room.

## Onboarding flow

1. Owner opens `/superadmin/?key=<SUPER_ADMIN_TOKEN>`, enters the new
   streamer's TikTok username.
2. Backend generates a `TenantID`, `OverlayToken`, and `AdminToken`,
   and appends the tenant to `tenants.json`.
3. Owner manually sends the streamer two links: their overlay URL (for
   OBS) and their admin CMS URL.

## Request routing

- **Overlay-facing** (`/ws`, `/overlay/`, `/api/lyrics`, `/api/advance`,
  `/api/queue`, `/api/stream`): tenant resolved from `OverlayToken` in
  `?key=`.
- **Admin-facing** (`/admin/`, `/api/config`, `/api/pending`,
  `/api/admin/*`): tenant resolved from `AdminToken`.
- **`/api/comment`** (called by `tiktok-connector`): the connector adds
  a `roomTikTokUsername` field to the POST body — the TikTok username of
  the room the comment came from, distinct from `username` (who posted
  the comment). Backend looks up the tenant whose `TikTokUsername`
  matches.
- **`tiktok-connector`** polls a new `GET /api/tenants` (authenticated
  with `BACKEND_SHARED_SECRET`) for the current list of TikTok usernames
  to watch, diffs it against the connections it currently holds, and
  connects/disconnects individual `TikTokLiveConnection`s to match —
  extending the existing single-username polling loop it already has.

## Data model changes

- New `Tenant` struct bundling `ID`, `TikTokUsername`, `OverlayToken`,
  `AdminToken`, `Queue`, `PendingQueue`, `Player`, `Hub`, `Config`
  (config becomes per-tenant: broadcaster username, auto-approve — note
  `TikTokUsername` itself now lives on `Tenant`, not inside `Config`,
  since it's also the registry's lookup key).
- `TenantRegistry`: loads/saves `tenants.json` on the Railway volume,
  owns the `map[TenantID]*Tenant`, and provides lookup by `OverlayToken`,
  `AdminToken`, and `TikTokUsername`.
- Every existing HTTP handler that currently closes over a single global
  `queue`/`hub`/`player`/`cfg` instead resolves a `*Tenant` first (via
  the appropriate token) and operates on that tenant's instances.

## tiktok-connector changes

- Replace the single `TikTokLiveConnection` + single-username polling
  with a `Map<tiktokUsername, connection>`.
- New poll loop hits `GET /api/tenants` every 30s (reusing the existing
  polling interval/pattern), diffs the returned username list against
  currently-held connections: connects new ones, disconnects removed
  ones.
- `forwardComment` includes `roomTikTokUsername` (the username of the
  connection the comment came from) alongside the existing `username`,
  `comment`, and `msgId` fields.

## Out of scope for this spec (explicitly deferred)

- Redis / moving off in-memory queue state.
- Self-service tenant signup.
- Billing.
- Per-tenant resource limits or rate limiting (a noisy tenant could
  still affect others sharing the process — acceptable for the current
  scale, revisit if it becomes a problem).
