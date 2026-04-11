package logging

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const ginRequestSummaryKey = "__request_summary__"

// RequestSummary accumulates per-request details from various components
// (conductor, translator) to produce a single summary log line on completion.
type RequestSummary struct {
	mu     sync.Mutex
	auth   string // e.g. "OAuth user6mq@gmail.com"
	route  string // "sticky" or "random"
	model  string
	tokens string // e.g. "input_tokens: 224649, output_tokens: 201, ..."
}

// SetAuth records which auth credential was selected and how.
func (s *RequestSummary) SetAuth(authDesc, routeMode, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = authDesc
	s.route = routeMode
	s.model = model
}

// SetTokenUsage records the token usage summary.
func (s *RequestSummary) SetTokenUsage(usage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = usage
}

// String builds the summary suffix for the completed log line.
func (s *RequestSummary) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var parts []string
	if s.auth != "" {
		authPart := s.auth
		if s.model != "" {
			authPart += " for " + s.model
		}
		if s.route != "" {
			authPart += " [" + s.route + "]"
		}
		parts = append(parts, authPart)
	}
	if s.tokens != "" {
		parts = append(parts, s.tokens)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ". ") + "."
}

// GetOrCreateSummary returns the RequestSummary for the current request,
// creating one if it doesn't exist yet.
func GetOrCreateSummary(c *gin.Context) *RequestSummary {
	if c == nil {
		return nil
	}
	if v, exists := c.Get(ginRequestSummaryKey); exists {
		if s, ok := v.(*RequestSummary); ok {
			return s
		}
	}
	s := &RequestSummary{}
	c.Set(ginRequestSummaryKey, s)
	return s
}

// GetSummary returns the RequestSummary if one exists, nil otherwise.
func GetSummary(c *gin.Context) *RequestSummary {
	if c == nil {
		return nil
	}
	if v, exists := c.Get(ginRequestSummaryKey); exists {
		if s, ok := v.(*RequestSummary); ok {
			return s
		}
	}
	return nil
}

// GetSummaryFromContext extracts the RequestSummary from a context.Context
// by first retrieving the gin.Context.
func GetSummaryFromContext(ctx context.Context) *RequestSummary {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return nil
	}
	return GetOrCreateSummary(ginCtx)
}

// RecordTokenUsage is a convenience helper for recording token usage from translator/executor code.
func RecordTokenUsage(ctx context.Context, format string, args ...any) {
	if s := GetSummaryFromContext(ctx); s != nil {
		s.SetTokenUsage(fmt.Sprintf(format, args...))
	}
}
