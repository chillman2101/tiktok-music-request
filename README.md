# TikTok Live Song Request Overlay

Backend Go + sidecar Python (ytmusicapi) + sidecar Node (TikTok Live connector)
+ overlay HTML untuk fitur song request di TikTok Live. Seluruh pipeline
jalan penuh: comment TikTok live → parsing command → music search → queue →
WebSocket → overlay (nampilin info + benar-benar memutar lagu via YouTube).

## Struktur

```
backend/           Go server: command parser, queue, WebSocket hub, panggil sidecar buat search
backend/overlay/   index.html — di-embed langsung ke binary Go (go:embed), diserve di /overlay/
music-search/      Sidecar Python (Flask + ytmusicapi) — resolve judul/artist/videoId dari query bebas
tiktok-connector/  Sidecar Node (tiktok-live-connector) — forward comment TikTok live asli ke backend
```

Overlay sengaja ditaruh di dalam `backend/` (bukan folder terpisah di root) dan
di-embed ke binary lewat `go:embed` — supaya kalau backend di-deploy dengan
root directory `backend/` (misalnya di Railway), file overlay-nya tetap ikut
kebawa dan nggak 404, nggak peduli apapun working directory servernya.

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
BROADCASTER_USERNAME=nama_tiktok_kamu \
BACKEND_SHARED_SECRET=isi_dengan_string_acak_panjang \
OVERLAY_TOKEN=isi_dengan_string_acak_panjang_lain \
go run .
```

**Terminal 3 — TikTok Live connector (nyala bareng pas kamu live):**
```bash
cd tiktok-connector
npm install
TIKTOK_USERNAME=nama_tiktok_kamu \
BACKEND_SHARED_SECRET=samain_dengan_punya_backend \
node index.js
```
Ini connect ke room live kamu (harus lagi live!) dan forward tiap comment
ke `/api/comment` backend secara otomatis — persis kayak curl manual yang
dijelaskan di bawah, tapi real-time dari viewer beneran.

Lalu pasang `http://localhost:8080/overlay/?key=isi_dengan_OVERLAY_TOKEN`
sebagai **Browser Source** di OBS (token-nya harus sama persis dengan
`OVERLAY_TOKEN` di atas). Di properties Browser Source, centang
**"Control audio via OBS"** — tanpa ini, sebagian besar browser (termasuk
yang dipakai OBS) memblokir autoplay audio dan lagu nggak akan bunyi.

### Soal keamanan (`BACKEND_SHARED_SECRET`, `OVERLAY_TOKEN`, `ADMIN_TOKEN`)

Tiga env var ini **opsional tapi sangat disarankan begitu backend bisa
diakses dari internet** (misal habis deploy ke Railway):

- **`BACKEND_SHARED_SECRET`** — dicek di `/api/comment` lewat header
  `Authorization: Bearer <secret>`. Tanpa ini, siapa aja yang tau URL
  backend kamu bisa POST comment palsu, spam `!play`, atau (kalau tau/tebak
  username broadcaster kamu) trigger `!skip`/`!clearqueue`. Harus **sama
  persis** di backend dan di `tiktok-connector`.
- **`OVERLAY_TOKEN`** — dicek di `/overlay/`, `/ws`, `/api/queue`, dan
  `/api/advance` lewat query param `?key=...`. Tanpa ini, URL overlay
  publik bisa dibuka siapa aja yang tau linknya.
- **`ADMIN_TOKEN`** — dicek di `/admin/` (CMS) dan `/api/config`,
  `/api/pending` lewat query param `?key=...`. Ini yang paling sensitif —
  siapapun yang punya token ini bisa ganti broadcaster username dan
  approve/reject request, jadi jangan dipakai bareng token lain.

Kalau env var-nya nggak diisi, backend tetap jalan (buat kemudahan testing
lokal) tapi bakal nge-log warning di startup bahwa endpoint itu terbuka.
Isi ketiganya dengan string acak yang panjang dan berbeda-beda (misal
`openssl rand -hex 32`), bukan kata yang gampang ditebak.

### CMS Admin (`/admin/`)

Buka `http://localhost:8080/admin/?key=<ADMIN_TOKEN>` (atau versi Railway-nya)
buat:

- **Ganti broadcaster username & TikTok username yang di-watch** tanpa perlu
  redeploy — `tiktok-connector` polling `/api/config` tiap 30 detik dan
  otomatis reconnect ke room baru kalau `TikTokUsername`-nya berubah.
- **Toggle auto-approve**: kalau nyala (default), `!play` langsung masuk
  antrian seperti biasa. Kalau dimatikan, tiap `!play` masuk ke daftar
  "menunggu approval" dulu — kamu approve/tolak manual lewat CMS sebelum
  masuk queue beneran (dan sebelum kelihatan di overlay).

Setting-an ini disimpan di `backend/config.json` (persist antar restart
proses, tapi nggak persist lintas redeploy Railway kecuali dipasangin
volume — sama kayak keterbatasan in-memory queue).

Panel "Sedang diputar & antrian" di CMS yang sama juga bisa buat:
- **Tambah lagu manual** (nggak lewat chat sama sekali — langsung dari CMS)
- **Skip** lagu yang lagi diputar
- **Hapus** satu lagu spesifik dari antrian
- **Clear semua** antrian
- **Drag-and-drop** buat geser urutan antrian (kecuali lagu yang lagi diputar)

### Soal duplikat comment pas redeploy

Kalau kamu notice ada `!play`/`!skip` yang keproses dua kali pas abis
redeploy — itu bukan gara-gara filter 10 detik di `tiktok-connector`
(itu cuma buat nyaring backlog abis reconnect network putus, bukan
buat kasus ini). Penyebabnya: Railway (dan platform serupa) sering
jalanin container lama & baru **bersamaan sebentar** pas rolling deploy,
jadi dua proses `tiktok-connector` sempat connect ke room yang sama dan
forward comment live yang sama persis, dari dua proses independen yang
nggak saling tau.

Fix-nya: dedup sekarang dipusatkan di **backend** (bukan di connector),
pakai `msgId` asli dari TikTok yang dikirim di setiap request ke
`/api/comment`. Karena backend cuma ada satu instance yang nerima dari
kedua proses connector itu, duplikat dari overlap-deploy ke-detect di
situ — bukan lagi mengandalkan Set di memory tiap proses connector yang
independen satu sama lain.

### Deploy ke Railway (3 service dari repo yang sama)

Buat 3 service, masing-masing dengan **root directory** yang beda:

| Service       | Root directory     | Publicly exposed? |
|---------------|---------------------|---------|
| backend       | `backend`           | Ya — ini yang jadi overlay URL |
| music-search  | `music-search`       | Tidak — internal only |
| tiktok-connector | `tiktok-connector` | Tidak — outbound only, gak perlu port |

Env vars:
- **backend**: `BROADCASTER_USERNAME`, `TIKTOK_USERNAME` (default awal, bisa
  diganti belakangan lewat CMS), `BACKEND_SHARED_SECRET`, `OVERLAY_TOKEN`,
  `ADMIN_TOKEN`, dan
  `MUSIC_SEARCH_URL=http://music-search.railway.internal:${{music-search.PORT}}/search`
  (pakai referensi variabel Railway biar port-nya otomatis sinkron)
- **tiktok-connector**: `TIKTOK_USERNAME`, `BACKEND_SHARED_SECRET` (samain
  dengan punya backend), `BACKEND_URL=https://<backend-service>.up.railway.app/api/comment`

Overlay URL buat OBS jadi:
`https://<backend-service>.up.railway.app/overlay/?key=<OVERLAY_TOKEN>`

CMS URL buat kamu sendiri:
`https://<backend-service>.up.railway.app/admin/?key=<ADMIN_TOKEN>`

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
