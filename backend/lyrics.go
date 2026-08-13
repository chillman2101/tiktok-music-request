package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// LyricLine is one timed line of lyrics, in seconds from the start of the
// track — mirrors LyricsOverlay's LyricLine model (D:\belajar\lyrics),
// just using float seconds instead of TimeSpan since that's what a
// JS <audio> element's currentTime gives us.
type LyricLine struct {
	Time float64 `json:"time"`
	Text string  `json:"text"`
}

type lrclibRecord struct {
	SyncedLyrics string `json:"syncedLyrics"`
	PlainLyrics  string `json:"plainLyrics"`
}

var lrclibClient = &http.Client{}

var lrcTimestampRe = regexp.MustCompile(`\[(\d{1,2}):(\d{2})(?:[.:](\d{1,3}))?\]`)

// lyricsCache avoids re-hitting LRCLIB for the same title+artist within a
// single process lifetime. Small and unbounded (song catalogs during a
// stream are naturally limited); resets on restart like everything else
// in-memory here.
var (
	lyricsCacheMu sync.Mutex
	lyricsCache   = map[string][]LyricLine{}
)

// FetchLyrics looks up synced lyrics from LRCLIB (https://lrclib.net, the
// same source used by D:\belajar\lyrics) by title/artist. Returns an empty
// slice (not an error) when no synced lyrics are found — most songs on
// LRCLIB either have full sync data or nothing, and "no lyrics" is a
// normal, common outcome, not a failure.
func FetchLyrics(title, artist string) []LyricLine {
	cacheKey := strings.ToLower(title) + "|" + strings.ToLower(artist)

	lyricsCacheMu.Lock()
	if cached, ok := lyricsCache[cacheKey]; ok {
		lyricsCacheMu.Unlock()
		return cached
	}
	lyricsCacheMu.Unlock()

	lines := searchLRCLIB(title, artist)

	lyricsCacheMu.Lock()
	lyricsCache[cacheKey] = lines
	lyricsCacheMu.Unlock()

	return lines
}

func searchLRCLIB(title, artist string) []LyricLine {
	q := url.Values{"track_name": {title}}
	if artist != "" {
		q.Set("artist_name", artist)
	}
	endpoint := "https://lrclib.net/api/search?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "tiktok-song-request/1.0 (personal use)")

	resp, err := lrclibClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var results []lrclibRecord
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil
	}

	for _, r := range results {
		if r.SyncedLyrics != "" {
			return parseLRC(r.SyncedLyrics)
		}
	}
	return nil
}

// parseLRC parses standard [mm:ss.xx] LRC-format synced lyrics text.
func parseLRC(lrc string) []LyricLine {
	var lines []LyricLine

	for _, raw := range strings.Split(lrc, "\n") {
		matches := lrcTimestampRe.FindAllStringSubmatch(raw, -1)
		if len(matches) == 0 {
			continue
		}

		text := strings.TrimSpace(lrcTimestampRe.ReplaceAllString(raw, ""))
		if text == "" {
			continue
		}

		for _, m := range matches {
			minutes, err1 := strconv.Atoi(m[1])
			seconds, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil {
				continue
			}
			fraction := 0.0
			if m[3] != "" {
				digits := m[3]
				for len(digits) < 3 {
					digits += "0"
				}
				if f, err := strconv.Atoi(digits); err == nil {
					fraction = float64(f) / 1000
				}
			}
			lines = append(lines, LyricLine{
				Time: float64(minutes*60+seconds) + fraction,
				Text: text,
			})
		}
	}

	return lines
}

// HandleLyrics serves GET /api/lyrics?title=...&artist=...
func HandleLyrics(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	artist := r.URL.Query().Get("artist")
	if title == "" {
		http.Error(w, "missing title", http.StatusBadRequest)
		return
	}

	lines := FetchLyrics(title, artist)
	if lines == nil {
		lines = []LyricLine{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"lines": lines})
}
