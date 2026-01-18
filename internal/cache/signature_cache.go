package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// SignatureEntry holds a cached thinking signature with timestamp
type SignatureEntry struct {
	Signature string
	Timestamp time.Time
}

const (
	// SignatureCacheTTL is how long signatures are valid
	SignatureCacheTTL = 2 * time.Hour

	// MaxEntriesPerSession limits memory usage per session
	MaxEntriesPerSession = 100

	// SignatureTextHashLen is the length of the hash key (16 hex chars = 64-bit key space)
	SignatureTextHashLen = 16

	// MinValidSignatureLen is the minimum length for a signature to be considered valid
	MinValidSignatureLen = 50

	// SessionCleanupInterval controls how often stale sessions are purged
	SessionCleanupInterval = 10 * time.Minute
)

// signatureCache stores signatures by sessionId -> textHash -> SignatureEntry
var signatureCache sync.Map

// sessionCleanupOnce ensures the background cleanup goroutine starts only once
var sessionCleanupOnce sync.Once

// sessionCache is the inner map type
type sessionCache struct {
	mu      sync.RWMutex
	entries map[string]SignatureEntry
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateSession gets or creates a session cache
func getOrCreateSession(sessionID string) *sessionCache {
	// Start background cleanup on first access
	sessionCleanupOnce.Do(startSessionCleanup)

	if val, ok := signatureCache.Load(sessionID); ok {
		return val.(*sessionCache)
	}
	sc := &sessionCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(sessionID, sc)
	return actual.(*sessionCache)
}

// startSessionCleanup launches a background goroutine that periodically
// removes sessions where all entries have expired.
func startSessionCleanup() {
	go func() {
		ticker := time.NewTicker(SessionCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredSessions()
		}
	}()
}

// purgeExpiredSessions removes sessions with no valid (non-expired) entries.
func purgeExpiredSessions() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*sessionCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, k)
			}
		}
		isEmpty := len(sc.entries) == 0
		sc.mu.Unlock()
		// Remove session if empty
		if isEmpty {
			signatureCache.Delete(key)
		}
		return true
	})
}

// CacheSignature stores a thinking signature for a given session and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(sessionID, text, signature string) {
	if sessionID == "" || text == "" || signature == "" {
		return
	}
	if len(signature) < MinValidSignatureLen {
		return
	}

	sc := getOrCreateSession(sessionID)
	textHash := hashText(text)

	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
	}
}

// GetCachedSignature retrieves a cached signature for a given session and text.
// Returns empty string if not found or expired.
func GetCachedSignature(sessionID, text string) string {
	if sessionID == "" || text == "" {
		return ""
	}

	val, ok := signatureCache.Load(sessionID)
	if !ok {
		return ""
	}
	sc := val.(*sessionCache)

	textHash := hashText(text)

	now := time.Now()

	sc.mu.Lock()
	entry, exists := sc.entries[textHash]
	if !exists {
		sc.mu.Unlock()
		return ""
	}
	if now.Sub(entry.Timestamp) > SignatureCacheTTL {
		delete(sc.entries, textHash)
		sc.mu.Unlock()
		return ""
	}

	// Refresh TTL on access (sliding expiration).
	entry.Timestamp = now
	sc.entries[textHash] = entry
	sc.mu.Unlock()

	return entry.Signature
}

// ClearSignatureCache clears signature cache for a specific session or all sessions.
func ClearSignatureCache(sessionID string) {
	if sessionID != "" {
		signatureCache.Delete(sessionID)
	} else {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
	}
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(signature string) bool {
	return signature != "" && len(signature) >= MinValidSignatureLen
}

// ============================================================================
// Thinking Cache - Lưu trữ toàn bộ thinking text + signature theo thinkingID
// ============================================================================

// ThinkingEntry holds cached thinking content with signature
type ThinkingEntry struct {
	ThinkingText string
	Signature    string
	Timestamp    time.Time
}

const (
	// ThinkingCacheTTL là thời gian thinking cache còn hiệu lực (dài hơn signature cache)
	ThinkingCacheTTL = 2 * time.Hour

	// MaxThinkingEntriesPerSession giới hạn số thinking entries mỗi session
	MaxThinkingEntriesPerSession = 100

	// ThinkingIDLen là độ dài của thinkingID (32 hex chars = 128-bit)
	ThinkingIDLen = 32
)

// thinkingCache stores thinking by sessionId -> thinkingId -> ThinkingEntry
var thinkingCache sync.Map

// thinkingSessionCache là inner map type cho thinking cache
type thinkingSessionCache struct {
	mu      sync.RWMutex
	entries map[string]ThinkingEntry
}

// GenerateThinkingID tạo hash-based ID từ thinking text
func GenerateThinkingID(thinkingText string) string {
	h := sha256.Sum256([]byte(thinkingText))
	return hex.EncodeToString(h[:])[:ThinkingIDLen]
}

// CacheThinking lưu thinking content với signature theo thinkingID
// Note: Đã loại bỏ sessionID vì không cần thiết - chỉ cần thinkingID là đủ
func CacheThinking(thinkingID, thinkingText, signature string) {
	if thinkingID == "" || thinkingText == "" {
		return
	}

	entry := ThinkingEntry{
		ThinkingText: thinkingText,
		Signature:    signature,
		Timestamp:    time.Now(),
	}

	thinkingCache.Store(thinkingID, entry)
}

// GetCachedThinking lấy cached thinking entry theo thinkingID
// Trả về nil nếu không tìm thấy hoặc đã expired
func GetCachedThinking(thinkingID string) *ThinkingEntry {
	if thinkingID == "" {
		return nil
	}

	val, ok := thinkingCache.Load(thinkingID)
	if !ok {
		return nil
	}

	entry := val.(ThinkingEntry)

	// Check if expired
	if time.Since(entry.Timestamp) > ThinkingCacheTTL {
		thinkingCache.Delete(thinkingID)
		return nil
	}

	return &entry
}

// ClearThinkingCache xóa thinking cache cho một thinkingID cụ thể hoặc tất cả
func ClearThinkingCache(thinkingID string) {
	if thinkingID != "" {
		thinkingCache.Delete(thinkingID)
	} else {
		thinkingCache.Range(func(key, _ any) bool {
			thinkingCache.Delete(key)
			return true
		})
	}
}
