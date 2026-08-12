"""
Small HTTP sidecar around ytmusicapi (unofficial YouTube Music API).

Kenapa sidecar terpisah, bukan langsung dari Go:
- ytmusicapi cuma ada di Python, gak ada library resmi/matang di Go.
- Ini dipanggil oleh backend Go lewat HTTP, sama pola-nya kayak nanti
  TikTok connector (Node) manggil backend Go. Tiap bahasa pegang bagian
  yang paling matang library-nya di situ.

Jalanin:
    pip install -r requirements.txt
    python app.py

Endpoint:
    GET /search?q=<query>  ->  {"title": "...", "artist": "...", "videoId": "..."}
"""

from flask import Flask, request, jsonify
from ytmusicapi import YTMusic

app = Flask(__name__)

# YTMusic() tanpa auth file cukup buat search publik (gak perlu login).
yt = YTMusic()


@app.route("/search")
def search():
    query = request.args.get("q", "").strip()
    if not query:
        return jsonify({"error": "missing query param 'q'"}), 400

    try:
        results = yt.search(query, filter="songs", limit=1)
    except Exception as e:
        return jsonify({"error": f"ytmusicapi search failed: {e}"}), 502

    if not results:
        return jsonify({"error": f"no results for '{query}'"}), 404

    top = results[0]
    artists = top.get("artists") or []
    artist_name = artists[0]["name"] if artists else "Unknown Artist"

    return jsonify({
        "title": top.get("title", query),
        "artist": artist_name,
        "videoId": top.get("videoId", ""),
    })


@app.route("/health")
def health():
    return jsonify({"status": "ok"})


if __name__ == "__main__":
    # Port 5000 — matches MUSIC_SEARCH_URL default in the Go backend.
    app.run(host="0.0.0.0", port=5000)
