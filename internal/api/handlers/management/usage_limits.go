package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// GetUsageLimits trả về rate limit usage cho tất cả tài khoản Claude.
// Mỗi source (email/key) có usage riêng.
//
// GET /v0/management/usage/limits
func (h *Handler) GetUsageLimits(c *gin.Context) {
	store := usage.GetRateLimitStore()
	bySource := store.LatestBySource()

	if len(bySource) == 0 {
		c.JSON(http.StatusOK, gin.H{"accounts": []gin.H{}})
		return
	}

	accounts := make([]gin.H, 0, len(bySource))
	for source, record := range bySource {
		reset5h := ""
		if !record.Reset5h.IsZero() {
			reset5h = record.Reset5h.Format(time.RFC3339)
		}
		reset7d := ""
		if !record.Reset7d.IsZero() {
			reset7d = record.Reset7d.Format(time.RFC3339)
		}
		accounts = append(accounts, gin.H{
			"source":    source,
			"5h_usage":  round2(record.Utilization5h * 100),
			"5h_status": record.Status5h,
			"5h_reset":  reset5h,
			"7d_usage":  round2(record.Utilization7d * 100),
			"7d_status": record.Status7d,
			"7d_reset":  reset7d,
		})
	}

	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

// round2 làm tròn float đến 2 chữ số thập phân.
func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
