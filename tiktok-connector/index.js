// Connects to a real TikTok Live room and forwards every chat comment to
// the Go backend's POST /api/comment — the exact same endpoint used for
// manual curl testing (see README). This is the piece that makes the
// whole pipeline (chat -> parse -> search -> queue -> overlay) live.
//
// Uses tiktok-live-connector, an unofficial reverse-engineered client for
// TikTok's live WebSocket protocol (no official public API exists for
// this). It can break if TikTok changes their internal protocol; if
// connect() starts failing, check for a newer package version first:
//   npm update tiktok-live-connector
//
// Usage:
//   TIKTOK_USERNAME=your_tiktok_handle node index.js
//
// Env vars:
//   TIKTOK_USERNAME       required — initial TikTok handle to watch (without @), must be live.
//                          Can be changed later from the admin CMS (/admin/) without restarting
//                          this process — it polls /api/config and reconnects on change.
//   BACKEND_URL           optional — where to POST comments, defaults to http://localhost:8080/api/comment
//   BACKEND_SHARED_SECRET optional — must match the backend's BACKEND_SHARED_SECRET env var;
//                          required once the backend is publicly reachable, otherwise
//                          anyone who finds the URL can POST fake comments to it.

const { TikTokLiveConnection, WebcastEvent } = require('tiktok-live-connector');

const initialUsername = process.env.TIKTOK_USERNAME;
if (!initialUsername) {
  console.error('Missing TIKTOK_USERNAME env var. Usage: TIKTOK_USERNAME=your_handle node index.js');
  process.exit(1);
}

const backendUrl = process.env.BACKEND_URL || 'http://localhost:8080/api/comment';
const backendBase = backendUrl.replace(/\/api\/comment\/?$/, '');
const configUrl = `${backendBase}/api/config`;
const backendSecret = process.env.BACKEND_SHARED_SECRET || '';
if (!backendSecret) {
  console.warn('WARNING: BACKEND_SHARED_SECRET not set — requests to the backend are unauthenticated.');
}

let connection = null;
let currentUsername = null;
const processedComments = new Set();
const MAX_COMMENT_CACHE = 500;

async function forwardComment(uniqueId, comment, msgId) {
  try {
    const headers = { 'Content-Type': 'application/json' };
    if (backendSecret) {
      headers['Authorization'] = `Bearer ${backendSecret}`;
    }
    const res = await fetch(backendUrl, {
      method: 'POST',
      headers,
      // msgId lets the backend dedup centrally — important because a
      // rolling deploy can briefly run two connector instances at once,
      // both forwarding the same live comment; this process's own
      // in-memory processedComments Set can't catch a duplicate sent by
      // the *other* instance, but the single backend instance can.
      body: JSON.stringify({ username: uniqueId, comment, msgId }),
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      console.warn(`backend rejected comment from @${uniqueId}: ${res.status} ${text}`);
    }
  } catch (err) {
    console.error('failed to forward comment to backend:', err.message);
  }
}

function onChat(data) {
  // 1. Filter berdasarkan timestamp. data.common.createTime is a proto
  // string, in seconds since epoch.
  const commentTime = Number(data.common?.createTime || 0);
  const diff = (Date.now() - commentTime * 1000) / 1000;

  // Abaikan comment yang lebih dari 10 detik yang lalu
  if (diff > 10) {
    console.log(`⏭️ Skipping old comment (${Math.round(diff)}s ago)`);
    return;
  }

  // 2. Filter duplicate within *this* process — cheap early-exit for the
  // common case, but not sufficient on its own: see forwardComment's
  // comment on why the backend also dedups by msgId.
  const commentId = data.common?.msgId;
  if (commentId && processedComments.has(commentId)) {
    console.log(`⏭️ Skipping duplicate: ${commentId}`);
    return;
  }
  if (commentId) {
    processedComments.add(commentId);
    if (processedComments.size > MAX_COMMENT_CACHE) {
      const first = processedComments.values().next().value;
      processedComments.delete(first);
    }
  }

  // 3. Extract data. displayId is the TikTok @handle (uniqueId).
  const uniqueId = data.user?.displayId || data.user?.nickname || 'unknown';
  const comment = data.content || '';

  console.log(`💬 [${uniqueId}]: ${comment}`);
  forwardComment(uniqueId, comment, commentId);
}

function connectTo(username) {
  if (connection) {
    connection.disconnect().catch(() => {});
  }

  currentUsername = username;
  connection = new TikTokLiveConnection(username, {
    // By default the library replays a batch of recent chat history as
    // CHAT events right after connecting. Every deploy/restart (or a
    // username change from the CMS) reconnects from scratch, so without
    // this, every reconnect would re-fire old "!play" comments.
    processInitialData: false,
  });

  connection.connect()
    .then((state) => {
      console.log(`Connected to @${username}'s live room (roomId ${state.roomId}). Forwarding comments to ${backendUrl}`);
    })
    .catch((err) => {
      console.error(`Failed to connect to @${username}'s live room. Is the account currently live?`, err.message);
    });

  connection.on(WebcastEvent.CHAT, onChat);

  connection.on(WebcastEvent.DISCONNECTED, () => {
    console.warn('Disconnected from TikTok Live room. Retrying in 5s...');
    setTimeout(() => {
      if (currentUsername === username) {
        connection.connect().catch((err) => console.error('reconnect failed:', err.message));
      }
    }, 5000);
  });

  connection.on(WebcastEvent.ERROR, (err) => {
    console.error('connector error:', err);
  });
}

// Poll the backend for a changed TikTok username set from the admin CMS,
// and reconnect to the new room if it differs from the one we're watching.
// The admin CMS is the source of truth once it's been saved at least once;
// until then we keep using TIKTOK_USERNAME from the env.
async function pollConfig() {
  try {
    const headers = {};
    if (backendSecret) {
      headers['Authorization'] = `Bearer ${backendSecret}`;
    }
    const res = await fetch(configUrl, { headers });
    if (!res.ok) return;
    const cfg = await res.json();
    if (cfg.tiktokUsername && cfg.tiktokUsername !== currentUsername) {
      console.log(`🔄 TikTok username changed via CMS: ${currentUsername} -> ${cfg.tiktokUsername}`);
      connectTo(cfg.tiktokUsername);
    }
  } catch (err) {
    console.warn('failed to poll /api/config:', err.message);
  }
}

connectTo(initialUsername);
setInterval(pollConfig, 30000);
