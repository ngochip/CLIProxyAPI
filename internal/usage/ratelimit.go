package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// rateLimitFilePath chứa đường dẫn file lưu rate limit statistics.
var rateLimitFilePath atomic.Value

// rlAutoSaveCancel dùng để cancel auto-save goroutine cho rate limit
var rlAutoSaveCancel context.CancelFunc
var rlAutoSaveMu sync.Mutex

// SetRateLimitFilePath đặt đường dẫn file lưu rate limit statistics.
func SetRateLimitFilePath(path string) {
	rateLimitFilePath.Store(path)
}

// GetRateLimitFilePath trả về đường dẫn file lưu rate limit statistics.
func GetRateLimitFilePath() string {
	if v := rateLimitFilePath.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// RateLimitRecord lưu 1 snapshot rate limit từ Claude API response headers.
// Hỗ trợ 2 format:
//   - Unified (OAuth/subscription): Anthropic-Ratelimit-Unified-5h-*, Anthropic-Ratelimit-Unified-7d-*
//   - Standard (API key): anthropic-ratelimit-requests-*, anthropic-ratelimit-tokens-*
type RateLimitRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"` // auth email/key identifier
	Model     string    `json:"model"`
	Type      string    `json:"type"` // "unified" hoặc "standard"

	// === Unified fields (OAuth/subscription) ===
	// 5-hour window
	Utilization5h float64   `json:"utilization_5h,omitempty"` // % đã dùng (0.0 - 1.0)
	Status5h      string    `json:"status_5h,omitempty"`      // "allowed" / "rejected"
	Reset5h       time.Time `json:"reset_5h,omitempty"`       // thời điểm reset

	// 7-day window
	Utilization7d float64   `json:"utilization_7d,omitempty"`
	Status7d      string    `json:"status_7d,omitempty"`
	Reset7d       time.Time `json:"reset_7d,omitempty"`

	// Unified overall
	UnifiedStatus         string    `json:"unified_status,omitempty"` // "allowed" / "rejected"
	UnifiedReset          time.Time `json:"unified_reset,omitempty"`
	RepresentativeClaim   string    `json:"representative_claim,omitempty"`    // "five_hour" / "seven_day"
	FallbackPercentage    float64   `json:"fallback_percentage,omitempty"`     // 0.5 = 50%
	OverageStatus         string    `json:"overage_status,omitempty"`          // "rejected" / "allowed"
	OverageDisabledReason string    `json:"overage_disabled_reason,omitempty"` // "org_level_disabled"
	OrganizationID        string    `json:"organization_id,omitempty"`

	// === Standard fields (API key) ===
	RequestsLimit         int64     `json:"requests_limit,omitempty"`
	RequestsRemaining     int64     `json:"requests_remaining,omitempty"`
	RequestsReset         time.Time `json:"requests_reset,omitempty"`
	TokensLimit           int64     `json:"tokens_limit,omitempty"`
	TokensRemaining       int64     `json:"tokens_remaining,omitempty"`
	TokensReset           time.Time `json:"tokens_reset,omitempty"`
	InputTokensLimit      int64     `json:"input_tokens_limit,omitempty"`
	InputTokensRemaining  int64     `json:"input_tokens_remaining,omitempty"`
	InputTokensReset      time.Time `json:"input_tokens_reset,omitempty"`
	OutputTokensLimit     int64     `json:"output_tokens_limit,omitempty"`
	OutputTokensRemaining int64     `json:"output_tokens_remaining,omitempty"`
	OutputTokensReset     time.Time `json:"output_tokens_reset,omitempty"`
}

// IsEmpty kiểm tra xem record có chứa dữ liệu rate limit hợp lệ không.
func (r RateLimitRecord) IsEmpty() bool {
	if r.Type == "unified" {
		return r.Status5h == "" && r.Status7d == "" && r.UnifiedStatus == ""
	}
	return r.RequestsLimit == 0 && r.TokensLimit == 0 && r.InputTokensLimit == 0 && r.OutputTokensLimit == 0
}

// UnifiedSummary chứa aggregated usage cho unified rate limit (OAuth).
type UnifiedSummary struct {
	TotalRequests int64            `json:"total_requests"`
	LatestRecord  *RateLimitRecord `json:"latest_record,omitempty"` // record mới nhất
	// Giá trị từ record mới nhất (tiện cho client đọc nhanh)
	Utilization5h float64 `json:"utilization_5h"`      // % đã dùng 5h window
	Status5h      string  `json:"status_5h,omitempty"` // "allowed" / "rejected"
	Reset5h       string  `json:"reset_5h,omitempty"`  // RFC3339
	Utilization7d float64 `json:"utilization_7d"`      // % đã dùng 7d window
	Status7d      string  `json:"status_7d,omitempty"` // "allowed" / "rejected"
	Reset7d       string  `json:"reset_7d,omitempty"`  // RFC3339
	OverageStatus string  `json:"overage_status,omitempty"`
}

// SourceUsage chứa usage summary cho 1 source (auth email/key).
type SourceUsage struct {
	Requests    int64            `json:"requests"`
	LatestLimit *RateLimitRecord `json:"latest_limit,omitempty"`
}

// WindowSummary chứa aggregated usage cho 1 time window.
type WindowSummary struct {
	TotalRequests int64                  `json:"total_requests"`
	Unified       *UnifiedSummary        `json:"unified,omitempty"`      // Unified rate limit data (OAuth)
	LatestLimit   *RateLimitRecord       `json:"latest_limit,omitempty"` // Standard rate limit (API key)
	BySource      map[string]SourceUsage `json:"by_source,omitempty"`
}

// RateLimitStore lưu trữ in-memory các rate limit records với JSON persistence.
type RateLimitStore struct {
	mu      sync.RWMutex
	records []RateLimitRecord
}

var defaultRateLimitStore = NewRateLimitStore()

// GetRateLimitStore trả về global singleton store.
func GetRateLimitStore() *RateLimitStore { return defaultRateLimitStore }

// NewRateLimitStore tạo store mới.
func NewRateLimitStore() *RateLimitStore {
	return &RateLimitStore{}
}

// maxRecordAge giới hạn records được giữ trong memory (7 ngày).
const maxRecordAge = 7 * 24 * time.Hour

// Record thêm 1 rate limit record vào store.
func (s *RateLimitStore) Record(r RateLimitRecord) {
	if s == nil || r.IsEmpty() {
		return
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}

	s.mu.Lock()
	s.records = append(s.records, r)
	// Cleanup records cũ hơn 7 ngày mỗi 100 records
	if len(s.records)%100 == 0 {
		s.cleanupLocked()
	}
	count := len(s.records)
	s.mu.Unlock()

	// Auto-save sau mỗi 10 records
	if count%10 == 0 {
		go func() {
			_ = s.Save()
		}()
	}
}

// cleanupLocked xóa records cũ hơn maxRecordAge. Phải gọi trong lock.
func (s *RateLimitStore) cleanupLocked() {
	cutoff := time.Now().Add(-maxRecordAge)
	n := 0
	for _, r := range s.records {
		if r.Timestamp.After(cutoff) {
			s.records[n] = r
			n++
		}
	}
	s.records = s.records[:n]
}

// Latest trả về record mới nhất (nil nếu chưa có).
func (s *RateLimitStore) Latest() *RateLimitRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.records) == 0 {
		return nil
	}
	r := s.records[len(s.records)-1]
	return &r
}

// LatestBySource trả về record mới nhất cho mỗi source (email/key).
func (s *RateLimitStore) LatestBySource() map[string]*RateLimitRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.records) == 0 {
		return nil
	}
	result := make(map[string]*RateLimitRecord)
	for i := len(s.records) - 1; i >= 0; i-- {
		r := s.records[i]
		source := r.Source
		if source == "" {
			source = "unknown"
		}
		if _, exists := result[source]; !exists {
			copy := r
			result[source] = &copy
		}
	}
	return result
}

// QueryByWindow trả về aggregated summary cho records trong time window.
func (s *RateLimitStore) QueryByWindow(d time.Duration) WindowSummary {
	summary := WindowSummary{
		BySource: make(map[string]SourceUsage),
	}
	if s == nil {
		return summary
	}

	cutoff := time.Now().Add(-d)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var latestTime time.Time
	var latestRecord *RateLimitRecord

	for i := range s.records {
		r := &s.records[i]
		if r.Timestamp.Before(cutoff) {
			continue
		}
		summary.TotalRequests++

		// Track latest record overall
		if r.Timestamp.After(latestTime) {
			latestTime = r.Timestamp
			rCopy := *r
			latestRecord = &rCopy
		}

		// Track per-source
		source := r.Source
		if source == "" {
			source = "unknown"
		}
		su := summary.BySource[source]
		su.Requests++
		if su.LatestLimit == nil || r.Timestamp.After(su.LatestLimit.Timestamp) {
			rCopy := *r
			su.LatestLimit = &rCopy
		}
		summary.BySource[source] = su
	}

	if latestRecord != nil {
		if latestRecord.Type == "unified" {
			summary.Unified = &UnifiedSummary{
				TotalRequests: summary.TotalRequests,
				LatestRecord:  latestRecord,
				Utilization5h: latestRecord.Utilization5h,
				Status5h:      latestRecord.Status5h,
				Utilization7d: latestRecord.Utilization7d,
				Status7d:      latestRecord.Status7d,
				OverageStatus: latestRecord.OverageStatus,
			}
			if !latestRecord.Reset5h.IsZero() {
				summary.Unified.Reset5h = latestRecord.Reset5h.Format(time.RFC3339)
			}
			if !latestRecord.Reset7d.IsZero() {
				summary.Unified.Reset7d = latestRecord.Reset7d.Format(time.RFC3339)
			}
		} else {
			summary.LatestLimit = latestRecord
		}
	}

	return summary
}

// rateLimitSnapshot dùng cho JSON persistence.
type rateLimitSnapshot struct {
	Records []RateLimitRecord `json:"records"`
}

// Save lưu records ra file JSON.
func (s *RateLimitStore) Save() error {
	if s == nil {
		return nil
	}
	filePath := GetRateLimitFilePath()
	if filePath == "" {
		return nil
	}

	s.mu.RLock()
	// Chỉ lưu records trong 7 ngày gần nhất
	cutoff := time.Now().Add(-maxRecordAge)
	var filtered []RateLimitRecord
	for _, r := range s.records {
		if r.Timestamp.After(cutoff) {
			filtered = append(filtered, r)
		}
	}
	s.mu.RUnlock()

	snapshot := rateLimitSnapshot{Records: filtered}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ratelimit statistics: %w", err)
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Atomic write: write to temp file, then rename
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		// Fallback: ghi trực tiếp
		if directErr := os.WriteFile(filePath, data, 0o644); directErr != nil {
			return fmt.Errorf("failed to write ratelimit file: %w", directErr)
		}
		return nil
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		// Fallback: ghi trực tiếp (Docker file mount)
		if directErr := os.WriteFile(filePath, data, 0o644); directErr != nil {
			return fmt.Errorf("failed to write ratelimit file: %w", directErr)
		}
	}

	return nil
}

// Load đọc records từ file JSON và restore vào memory.
func (s *RateLimitStore) Load() error {
	if s == nil {
		return nil
	}
	filePath := GetRateLimitFilePath()
	if filePath == "" {
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read ratelimit file: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	var snapshot rateLimitSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("failed to unmarshal ratelimit statistics: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = snapshot.Records
	s.cleanupLocked()

	return nil
}

// StartRateLimitAutoSave bắt đầu auto-save rate limit statistics định kỳ.
func StartRateLimitAutoSave(ctx context.Context, interval time.Duration) {
	rlAutoSaveMu.Lock()
	defer rlAutoSaveMu.Unlock()

	if rlAutoSaveCancel != nil {
		rlAutoSaveCancel()
	}

	ctx, rlAutoSaveCancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = defaultRateLimitStore.Save()
			}
		}
	}()
}

// StopRateLimitAutoSave dừng auto-save và save lần cuối.
func StopRateLimitAutoSave() {
	rlAutoSaveMu.Lock()
	if rlAutoSaveCancel != nil {
		rlAutoSaveCancel()
		rlAutoSaveCancel = nil
	}
	rlAutoSaveMu.Unlock()

	_ = defaultRateLimitStore.Save()
}

// ParseRateLimitHeaders parse rate limit headers từ HTTP response của Claude API.
// Hỗ trợ 2 format: Unified (OAuth) và Standard (API key).
func ParseRateLimitHeaders(headers http.Header) RateLimitRecord {
	r := RateLimitRecord{
		Timestamp: time.Now(),
	}

	// Thử parse Unified format trước (OAuth/subscription)
	if parseUnifiedHeaders(headers, &r) {
		r.Type = "unified"
		return r
	}

	// Fallback: parse Standard format (API key)
	parseStandardHeaders(headers, &r)
	if !r.IsEmpty() {
		r.Type = "standard"
	}

	return r
}

// parseUnifiedHeaders parse Anthropic-Ratelimit-Unified-* headers.
// Trả về true nếu tìm thấy ít nhất 1 unified header.
func parseUnifiedHeaders(headers http.Header, r *RateLimitRecord) bool {
	found := false

	// Organization ID
	if v := headers.Get("Anthropic-Organization-Id"); v != "" {
		r.OrganizationID = v
		found = true
	}

	// 5-hour window
	if v := headers.Get("Anthropic-Ratelimit-Unified-5h-Utilization"); v != "" {
		r.Utilization5h = parseFloatHeader(v)
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-5h-Status"); v != "" {
		r.Status5h = strings.ToLower(strings.TrimSpace(v))
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-5h-Reset"); v != "" {
		r.Reset5h = parseUnixTimestamp(v)
		found = true
	}

	// 7-day window
	if v := headers.Get("Anthropic-Ratelimit-Unified-7d-Utilization"); v != "" {
		r.Utilization7d = parseFloatHeader(v)
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-7d-Status"); v != "" {
		r.Status7d = strings.ToLower(strings.TrimSpace(v))
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-7d-Reset"); v != "" {
		r.Reset7d = parseUnixTimestamp(v)
		found = true
	}

	// Unified overall
	if v := headers.Get("Anthropic-Ratelimit-Unified-Status"); v != "" {
		r.UnifiedStatus = strings.ToLower(strings.TrimSpace(v))
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-Reset"); v != "" {
		r.UnifiedReset = parseUnixTimestamp(v)
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); v != "" {
		r.RepresentativeClaim = strings.TrimSpace(v)
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-Fallback-Percentage"); v != "" {
		r.FallbackPercentage = parseFloatHeader(v)
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-Overage-Status"); v != "" {
		r.OverageStatus = strings.ToLower(strings.TrimSpace(v))
		found = true
	}
	if v := headers.Get("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason"); v != "" {
		r.OverageDisabledReason = strings.TrimSpace(v)
		found = true
	}

	return found
}

// parseStandardHeaders parse anthropic-ratelimit-* headers (API key format).
func parseStandardHeaders(headers http.Header, r *RateLimitRecord) {
	r.RequestsLimit = parseIntHeader(headers, "anthropic-ratelimit-requests-limit")
	r.RequestsRemaining = parseIntHeader(headers, "anthropic-ratelimit-requests-remaining")
	r.RequestsReset = parseRFC3339Header(headers, "anthropic-ratelimit-requests-reset")
	r.TokensLimit = parseIntHeader(headers, "anthropic-ratelimit-tokens-limit")
	r.TokensRemaining = parseIntHeader(headers, "anthropic-ratelimit-tokens-remaining")
	r.TokensReset = parseRFC3339Header(headers, "anthropic-ratelimit-tokens-reset")
	r.InputTokensLimit = parseIntHeader(headers, "anthropic-ratelimit-input-tokens-limit")
	r.InputTokensRemaining = parseIntHeader(headers, "anthropic-ratelimit-input-tokens-remaining")
	r.InputTokensReset = parseRFC3339Header(headers, "anthropic-ratelimit-input-tokens-reset")
	r.OutputTokensLimit = parseIntHeader(headers, "anthropic-ratelimit-output-tokens-limit")
	r.OutputTokensRemaining = parseIntHeader(headers, "anthropic-ratelimit-output-tokens-remaining")
	r.OutputTokensReset = parseRFC3339Header(headers, "anthropic-ratelimit-output-tokens-reset")
}

func parseIntHeader(headers http.Header, name string) int64 {
	v := headers.Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseFloatHeader(v string) float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseRFC3339Header(headers http.Header, name string) time.Time {
	v := headers.Get(name)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseUnixTimestamp parse Unix timestamp (seconds) thành time.Time.
func parseUnixTimestamp(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	// Thử parse float trước (có thể có decimal)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		// Fallback: thử parse RFC3339
		t, errRFC := time.Parse(time.RFC3339, v)
		if errRFC != nil {
			return time.Time{}
		}
		return t
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}
