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
//   TIKTOK_USERNAME   required — TikTok handle to watch (without @), must be live
//   BACKEND_URL       optional — where to POST comments, defaults to http://localhost:8080/api/comment

const { WebcastPushConnection } = require('tiktok-live-connector');

const username = process.env.TIKTOK_USERNAME;
if (!username) {
  console.error('Missing TIKTOK_USERNAME env var. Usage: TIKTOK_USERNAME=your_handle node index.js');
  process.exit(1);
}

const backendUrl = process.env.BACKEND_URL || 'http://localhost:8080/api/comment';

const connection = new WebcastPushConnection(username);

async function forwardComment(uniqueId, comment) {
  try {
    const res = await fetch(backendUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
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

connection.on('chat', (data) => {
  forwardComment(data.uniqueId, data.comment);
});

connection.on('disconnected', () => {
  console.warn('Disconnected from TikTok Live room. Retrying in 5s...');
  setTimeout(() => connection.connect().catch((err) => console.error('reconnect failed:', err.message)), 5000);
});

connection.on('error', (err) => {
  console.error('connector error:', err);
});
