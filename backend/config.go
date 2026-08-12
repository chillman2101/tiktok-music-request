package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// ConfigView is a plain snapshot of Config's data — safe to copy/return by
// value, unlike Config itself which holds a sync.Mutex.
type ConfigView struct {
	BroadcasterUsername string `json:"broadcasterUsername"`
	TikTokUsername      string `json:"tiktokUsername"`
	AutoApprove         bool   `json:"autoApprove"`
}

// Config holds settings that can be changed at runtime from the admin CMS,
// instead of being fixed at process start via env vars. Persisted to a JSON
// file so changes survive a process restart (but not a fresh Railway
// deployment without a mounted volume — same caveat as the in-memory queue).
type Config struct {
	mu   sync.Mutex
	path string
	data ConfigView
}

// NewConfig loads config from path if it exists, otherwise starts from the
// given env-var defaults (so first boot behaves like before the CMS existed).
func NewConfig(path, defaultBroadcaster, defaultTikTok string) *Config {
	c := &Config{
		path: path,
		data: ConfigView{
			BroadcasterUsername: defaultBroadcaster,
			TikTokUsername:      defaultTikTok,
			AutoApprove:         true,
		},
	}

	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &c.data); err != nil {
			log.Println("WARNING: failed to parse config file, using defaults:", err)
		}
	}

	return c
}

func (c *Config) Snapshot() ConfigView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data
}

// Update applies only the fields present in the request (empty string /
// nil means "leave unchanged") and persists to disk.
func (c *Config) Update(broadcaster, tiktok *string, autoApprove *bool) ConfigView {
	c.mu.Lock()
	defer c.mu.Unlock()

	if broadcaster != nil && *broadcaster != "" {
		c.data.BroadcasterUsername = *broadcaster
	}
	if tiktok != nil && *tiktok != "" {
		c.data.TikTokUsername = *tiktok
	}
	if autoApprove != nil {
		c.data.AutoApprove = *autoApprove
	}

	c.save()

	return c.data
}

// save must be called with mu held.
func (c *Config) save() {
	raw, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		log.Println("WARNING: failed to marshal config:", err)
		return
	}
	if err := os.WriteFile(c.path, raw, 0644); err != nil {
		log.Println("WARNING: failed to save config file:", err)
	}
}
