package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

	// CacheCleanupInterval controls how often stale entries are purged
	CacheCleanupInterval = 10 * time.Minute
)

// signatureCache stores signatures by model group -> textHash -> SignatureEntry
var signatureCache sync.Map

// cacheCleanupOnce ensures the background cleanup goroutine starts only once
var cacheCleanupOnce sync.Once

// groupCache is the inner map type
type groupCache struct {
	mu      sync.RWMutex
	entries map[string]SignatureEntry
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateGroupCache gets or creates a cache bucket for a model group
func getOrCreateGroupCache(groupKey string) *groupCache {
	// Start background cleanup on first access
	cacheCleanupOnce.Do(startCacheCleanup)

	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(groupKey, sc)
	return actual.(*groupCache)
}

// startCacheCleanup launches a background goroutine that periodically
// removes caches where all entries have expired.
func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(CacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCaches()
		}
	}()
}

// purgeExpiredCaches removes caches with no valid (non-expired) entries.
func purgeExpiredCaches() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				delete(sc.entries, k)
			}
		}
		isEmpty := len(sc.entries) == 0
		sc.mu.Unlock()
		// Remove cache bucket if empty
		if isEmpty {
			signatureCache.Delete(key)
		}
		return true
	})
}

// CacheSignature stores a thinking signature for a given model group and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(modelName, text, signature string) {
	if text == "" || signature == "" {
		return
	}
	if len(signature) < MinValidSignatureLen {
		return
	}

	groupKey := GetModelGroup(modelName)
	textHash := hashText(text)
	sc := getOrCreateGroupCache(groupKey)
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
	}
}

// GetCachedSignature retrieves a cached signature for a given model group and text.
// Returns empty string if not found or expired.
func GetCachedSignature(modelName, text string) string {
	groupKey := GetModelGroup(modelName)

	if text == "" {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	val, ok := signatureCache.Load(groupKey)
	if !ok {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	sc := val.(*groupCache)

	textHash := hashText(text)

	now := time.Now()

	sc.mu.Lock()
	entry, exists := sc.entries[textHash]
	if !exists {
		sc.mu.Unlock()
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}
	if now.Sub(entry.Timestamp) > SignatureCacheTTL {
		delete(sc.entries, textHash)
		sc.mu.Unlock()
		if groupKey == "gemini" {
			return "skip_thought_signature_validator"
		}
		return ""
	}

	// Refresh TTL on access (sliding expiration).
	entry.Timestamp = now
	sc.entries[textHash] = entry
	sc.mu.Unlock()

	return entry.Signature
}

// ClearSignatureCache clears signature cache for a specific model group or all groups.
func ClearSignatureCache(modelName string) {
	if modelName == "" {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
		return
	}
	groupKey := GetModelGroup(modelName)
	signatureCache.Delete(groupKey)
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(modelName, signature string) bool {
	return (signature != "" && len(signature) >= MinValidSignatureLen) || (signature == "skip_thought_signature_validator" && GetModelGroup(modelName) == "gemini")
}

func GetModelGroup(modelName string) string {
	if strings.Contains(modelName, "gpt") {
		return "gpt"
	} else if strings.Contains(modelName, "claude") {
		return "claude"
	} else if strings.Contains(modelName, "gemini") {
		return "gemini"
	}
	return modelName
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
