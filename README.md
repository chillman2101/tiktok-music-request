# TikTok Live Song Request Overlay

Backend Go + sidecar Python (ytmusicapi) + sidecar Node (TikTok Live connector)
+ overlay HTML untuk fitur song request di TikTok Live. Seluruh pipeline
jalan penuh: comment TikTok live → parsing command → music search → queue →
WebSocket → overlay (nampilin info + benar-benar memutar lagu via YouTube).

## Struktur

```
backend/           Go server: command parser, queue, WebSocket hub, panggil sidecar buat search
music-search/      Sidecar Python (Flask + ytmusicapi) — resolve judul/artist/videoId dari query bebas
tiktok-connector/  Sidecar Node (tiktok-live-connector) — forward comment TikTok live asli ke backend
overlay/           index.html — dipasang sebagai Browser Source di OBS, sekaligus jadi player YouTube
```

Kenapa ada tiga service terpisah: `ytmusicapi` cuma tersedia di Python, dan
client TikTok Live yang paling matang ada di Node — masing-masing bahasa
pegang bagian yang library-nya paling matang di situ, semua ngobrol ke
backend Go lewat HTTP.

## Cara jalanin buat live beneran (tiga service, tiga terminal)

**Terminal 1 — sidecar pencarian lagu:**
```bash
cd music-search
pip install -r requirements.txt
python app.py
```

**Terminal 2 — backend:**
```bash
cd backend
go mod tidy
BROADCASTER_USERNAME=nama_tiktok_kamu go run .
```

**Terminal 3 — TikTok Live connector (nyala bareng pas kamu live):**
```bash
cd tiktok-connector
npm install
TIKTOK_USERNAME=nama_tiktok_kamu node index.js
```
Ini connect ke room live kamu (harus lagi live!) dan forward tiap comment
ke `/api/comment` backend secara otomatis — persis kayak curl manual yang
dijelaskan di bawah, tapi real-time dari viewer beneran.

Lalu pasang `http://localhost:8080/overlay/` sebagai **Browser Source** di
OBS. Di properties Browser Source, centang **"Control audio via OBS"** —
tanpa ini, sebagian besar browser (termasuk yang dipakai OBS) memblokir
autoplay audio dan lagu nggak akan bunyi.

⚠️ `tiktok-live-connector` itu unofficial (reverse-engineered), bisa putus
kalau TikTok ubah protokol internal mereka — kalau connect gagal, coba
`npm update tiktok-live-connector` dulu.

⚠️ Overlay memutar audio YouTube langsung di browser source. Ini pendekatan
paling umum dipakai buat song-request overlay, tapi secara teknis termasuk
re-broadcast audio berhak cipta — ada risiko copyright claim/strike kalau
channel makin besar. Kalau butuh yang lebih aman, ganti sumber lagu ke
library musik berlisensi untuk streaming.

## Cara jalanin buat testing lokal (tanpa TikTok live, dua terminal)

**Terminal 1 — sidecar pencarian lagu:**
```bash
cd music-search
pip install -r requirements.txt
python app.py
```
Jalan di `http://localhost:5000`.

**Terminal 2 — backend:**
```bash
cd backend
go mod tidy
go run .
```
Jalan di `http://localhost:8080`. Log akan nampilin URL overlay
buat dipasang di OBS: `http://localhost:8080/overlay/`

Backend otomatis manggil sidecar di `http://localhost:5000/search` — bisa
di-override lewat env var `MUSIC_SEARCH_URL` kalau jalanin di port/host lain.

## Cara test tanpa TikTok live beneran

Endpoint `/api/comment` adalah tempat comment chat masuk. Untuk sekarang,
kamu simulasikan sendiri pakai `curl` (nanti ini yang akan dipanggil
otomatis oleh TikTok connector).

**1. Buka overlay di browser** (atau tambahkan sebagai Browser Source
di OBS): `http://localhost:8080/overlay/`

**2. Simulasikan orang request lagu:**
```bash
curl -X POST localhost:8080/api/comment \
  -d '{"username":"budi","comment":"!play Payung Teduh - Akad"}'
```
Overlay akan langsung update nampilin "Now Playing: Akad — Payung Teduh".

**3. Tambah beberapa lagu lagi buat lihat antrian:**
```bash
curl -X POST localhost:8080/api/comment \
  -d '{"username":"sari","comment":"!play Sheila On 7 - Dan"}'
```

**4. Test skip (cuma broadcaster yang boleh):**
```bash
# ini akan ditolak (403) karena bukan broadcaster
curl -X POST localhost:8080/api/comment \
  -d '{"username":"random","comment":"!skip"}'

# ini akan berhasil
curl -X POST localhost:8080/api/comment \
  -d '{"username":"broadcaster","comment":"!skip"}'
```
Set username broadcaster kamu sendiri lewat env var:
```bash
BROADCASTER_USERNAME=nama_tiktok_kamu go run .
```

**5. Cek queue mentah kapan aja:**
```bash
curl localhost:8080/api/queue
```

## Tentang ytmusicapi

`ytmusicapi` itu unofficial — dia reverse-engineer endpoint internal YouTube
Music, bukan API resmi Google. Konsekuensinya:

- **Gak ada limit kuota** kayak YouTube Data API resmi (jadi gak perlu
  khawatir soal 100 search/hari), dan gak butuh API key.
- Tapi bisa berubah/rusak sewaktu-waktu kalau YouTube Music ubah struktur
  internal mereka — perlu update `ytmusicapi` versi terbaru dari waktu ke
  waktu (`pip install -U ytmusicapi`).
- `music-search/app.py` menangani error dari ytmusicapi dan balikin JSON
  error yang jelas ke backend Go — backend Go sendiri gak akan crash kalau
  sidecar ini error atau lagi gak jalan (sudah ditest: request gagal dengan
  pesan jelas, bukan panic).

## Langkah selanjutnya (belum diimplementasi)

- **Redis** buat gantiin in-memory queue, biar state gak hilang kalau
  server restart, dan biar bisa di-scale ke lebih dari satu instance.
- **Rate limiting** per user biar gak ada yang spam `!play` berkali-kali.
- **Cache hasil pencarian** (misal di Redis) biar gak nembak ytmusicapi
  berkali-kali buat query yang sama, dan lebih tahan kalau sidecar lagi
  lambat/rate-limited oleh YouTube Music.
