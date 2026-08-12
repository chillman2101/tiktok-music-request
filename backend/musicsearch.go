package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

// SongResult is what we need from a search: just title + artist.
type SongResult struct {
	Title   string
	Artist  string
	VideoID string
}

type searchServiceResponse struct {
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	VideoID string `json:"videoId"`
	Error   string `json:"error"`
}

// ResolveSong calls the local Python sidecar (music-search/app.py), which
// wraps ytmusicapi (unofficial YouTube Music API — no quota, no API key,
// but unofficial: it can break if YouTube Music changes internally).
//
// Sidecar URL is configurable via MUSIC_SEARCH_URL, defaults to the
// standard local port used by music-search/app.py.
func ResolveSong(query string) (SongResult, error) {
	base := os.Getenv("MUSIC_SEARCH_URL")
	if base == "" {
		base = "http://localhost:5000/search"
	}

	endpoint := base + "?" + url.Values{"q": {query}}.Encode()

	resp, err := http.Get(endpoint)
	if err != nil {
		return SongResult{}, fmt.Errorf("music search request failed (is music-search/app.py running?): %w", err)
	}
	defer resp.Body.Close()

	var parsed searchServiceResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return SongResult{}, fmt.Errorf("failed to decode music search response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := parsed.Error
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return SongResult{}, fmt.Errorf("music search error: %s", msg)
	}

	return SongResult{
		Title:   parsed.Title,
		Artist:  parsed.Artist,
		VideoID: parsed.VideoID,
	}, nil
}
