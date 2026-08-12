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
//   TIKTOK_USERNAME       required — TikTok handle to watch (without @), must be live
//   BACKEND_URL           optional — where to POST comments, defaults to http://localhost:8080/api/comment
//   BACKEND_SHARED_SECRET optional — must match the backend's BACKEND_SHARED_SECRET env var;
//                          required once the backend is publicly reachable, otherwise
//                          anyone who finds the URL can POST fake comments to it.

const { TikTokLiveConnection, WebcastEvent } = require('tiktok-live-connector');

const username = process.env.TIKTOK_USERNAME;
if (!username) {
  console.error('Missing TIKTOK_USERNAME env var. Usage: TIKTOK_USERNAME=your_handle node index.js');
  process.exit(1);
}

const backendUrl = process.env.BACKEND_URL || 'http://localhost:8080/api/comment';
const backendSecret = process.env.BACKEND_SHARED_SECRET || '';
if (!backendSecret) {
  console.warn('WARNING: BACKEND_SHARED_SECRET not set — requests to the backend are unauthenticated.');
}

// Pass an explicit options object — some internal defaults in this
// library version destructure the second argument without defaulting
// it to {} themselves, so omitting it throws "Cannot read properties
// of undefined (reading 'processInitialData')".
//
// processInitialData: false is the important one here — by default the
// library replays a batch of *recent chat history* as CHAT events right
// after connecting. Every deploy/restart reconnects from scratch, so
// without this, every redeploy re-fires old "!play" comments from
// whatever was said in the last minute or so before the restart.
const connection = new TikTokLiveConnection(username, {
  processInitialData: false,
});

async function forwardComment(uniqueId, comment) {
  try {
    const headers = { 'Content-Type': 'application/json' };
    if (backendSecret) {
      headers['Authorization'] = `Bearer ${backendSecret}`;
    }
    const res = await fetch(backendUrl, {
      method: 'POST',
      headers,
      body: JSON.stringify({ username: uniqueId, comment }),
    });
    if (!res.ok && res.status !== 204) {
      const text = await res.text().catch(() => '');
      console.warn(`backend rejected comment from @${uniqueId}: ${res.status} ${text}`);
    }
  } catch (err) {
    console.error('failed to forward comment to backend:', err.message);
  }
}

connection.connect()
  .then((state) => {
    console.log(`Connected to @${username}'s live room (roomId ${state.roomId}). Forwarding comments to ${backendUrl}`);
  })
  .catch((err) => {
    console.error('Failed to connect to TikTok Live. Is the account currently live?', err);
    process.exit(1);
  });

// === FILTER OLD COMMENTS ===
let connectionStartTime = Date.now();
const processedComments = new Set();
const MAX_COMMENT_CACHE = 500;

connection.on(WebcastEvent.CHAT, (data) => {
  // 1. Filter berdasarkan timestamp
  const commentTime = data.createTime || data.common?.createTime || 0;
  const diff = (Date.now() - commentTime * 1000) / 1000;

  // Abaikan comment yang lebih dari 10 detik yang lalu
  if (diff > 10) {
    console.log(`⏭️ Skipping old comment (${Math.round(diff)}s ago)`);
    return;
  }

  // 2. Filter duplicate (by comment ID)
  const commentId = data.id || data.commentId || `${data.common?.displayId}_${data.content}`;
  if (processedComments.has(commentId)) {
    console.log(`⏭️ Skipping duplicate: ${commentId}`);
    return;
  }

  processedComments.add(commentId);
  if (processedComments.size > MAX_COMMENT_CACHE) {
    const first = processedComments.values().next().value;
    processedComments.delete(first);
  }

  // 3. Extract data
  const uniqueId = data.common?.displayId || data.user?.displayId || 'unknown';
  const comment = data.content || data.comment || '';

  console.log(`💬 [${uniqueId}]: ${comment}`);
  forwardComment(uniqueId, comment);
});

connection.on(WebcastEvent.DISCONNECTED, () => {
  console.warn('Disconnected from TikTok Live room. Retrying in 5s...');
  setTimeout(() => connection.connect().catch((err) => console.error('reconnect failed:', err.message)), 5000);
});

connection.on(WebcastEvent.ERROR, (err) => {
  console.error('connector error:', err);
});
