package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	DefaultStickySessionTTL      = 30 * time.Minute
	stickySessionCleanupInterval = 5 * time.Minute
	conversationKeyLen           = 16
)

type stickyEntry struct {
	AuthID    string
	ExpiresAt time.Time
}

// StickySessionStore maps conversation fingerprints to auth IDs for cache-optimal routing.
type StickySessionStore struct {
	mu      sync.RWMutex
	entries map[string]stickyEntry
	ttl     time.Duration
}

var globalStickyStore *StickySessionStore
var stickyStoreOnce sync.Once

// GetStickySessionStore returns the singleton sticky session store.
func GetStickySessionStore(ttl time.Duration) *StickySessionStore {
	stickyStoreOnce.Do(func() {
		if ttl <= 0 {
			ttl = DefaultStickySessionTTL
		}
		globalStickyStore = &StickySessionStore{
			entries: make(map[string]stickyEntry),
			ttl:     ttl,
		}
		go globalStickyStore.cleanupLoop()
	})
	return globalStickyStore
}

// ConversationKey builds a stable fingerprint from the request body.
// Uses hash of first user message content + metadata.user_id to uniquely identify a conversation.
func ConversationKey(rawJSON []byte) string {
	userID := gjson.GetBytes(rawJSON, "metadata.user_id").String()

	var firstUserContent string
	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "user" {
				content := msg.Get("content")
				if content.IsArray() && len(content.Array()) > 0 {
					firstUserContent = content.Array()[0].Get("text").String()
				} else if content.Type == gjson.String {
					firstUserContent = content.String()
				}
				break
			}
		}
	}

	if firstUserContent == "" && userID == "" {
		return ""
	}

	// Truncate first message to 512 chars for stable hashing
	if len(firstUserContent) > 512 {
		firstUserContent = firstUserContent[:512]
	}

	h := sha256.Sum256([]byte(userID + "|" + firstUserContent))
	return hex.EncodeToString(h[:])[:conversationKeyLen]
}

// Lookup returns the pinned auth ID for a conversation key, or empty string if not found/expired.
func (s *StickySessionStore) Lookup(convKey string) string {
	if convKey == "" {
		return ""
	}
	s.mu.RLock()
	entry, ok := s.entries[convKey]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return ""
	}
	return entry.AuthID
}

// Set stores or refreshes the mapping from conversation key to auth ID.
func (s *StickySessionStore) Set(convKey, authID string) {
	if convKey == "" || authID == "" {
		return
	}
	s.mu.Lock()
	s.entries[convKey] = stickyEntry{
		AuthID:    authID,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Unlock()
	log.WithFields(log.Fields{
		"conv_key": convKey[:8] + "...",
		"auth_id":  authID,
		"ttl":      s.ttl,
	}).Debug("sticky session: pinned conversation to auth")
}

// Remove deletes a sticky session entry (e.g., when pinned auth is unavailable).
func (s *StickySessionStore) Remove(convKey string) {
	if convKey == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, convKey)
	s.mu.Unlock()
}

// Size returns the current number of active sticky sessions.
func (s *StickySessionStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *StickySessionStore) cleanupLoop() {
	ticker := time.NewTicker(stickySessionCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.purgeExpired()
	}
}

func (s *StickySessionStore) purgeExpired() {
	now := time.Now()
	s.mu.Lock()
	for k, entry := range s.entries {
		if now.After(entry.ExpiresAt) {
			delete(s.entries, k)
		}
	}
	s.mu.Unlock()
}
