// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When logging is disabled in the
// logger, it still captures data so that upstream errors can be persisted.
func RequestLoggingMiddleware(logger logging.RequestLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}

		if c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

	// Bắt đầu tracking thời gian
	startTime := time.Now()

	// Capture request information
	requestInfo, err := captureRequestInfo(c)
	if err != nil {
		// Log error but continue processing
		log.WithFields(log.Fields{
			"request_id": logging.GetGinRequestID(c),
			"error":      err.Error(),
		}).Error("Failed to capture request info")
		c.Next()
		return
	}

	// Log ngay khi nhận request
	log.WithFields(log.Fields{
		"request_id": requestInfo.RequestID,
		"method":     requestInfo.Method,
		"path":       requestInfo.URL,
		"client_ip":  c.ClientIP(),
	}).Info("🔵 Request received")

	// Create response writer wrapper
	wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
	if !logger.IsEnabled() {
		wrapper.logOnErrorOnly = true
	}
	c.Writer = wrapper

	// Process the request
	c.Next()

	// Tính toán thời gian xử lý
	duration := time.Since(startTime)

	// Log khi request hoàn thành
	statusCode := c.Writer.Status()
	logEntry := log.WithFields(log.Fields{
		"request_id": requestInfo.RequestID,
		"method":     requestInfo.Method,
		"path":       requestInfo.URL,
		"status":     statusCode,
		"duration":   duration.String(),
		"duration_ms": duration.Milliseconds(),
	})

	if statusCode >= 500 {
		logEntry.Error("🔴 Request completed with server error")
	} else if statusCode >= 400 {
		logEntry.Warn("🟡 Request completed with client error")
	} else {
		logEntry.Info("🟢 Request completed successfully")
	}

	// Finalize logging after request processing
	if err = wrapper.Finalize(c); err != nil {
		log.WithFields(log.Fields{
			"request_id": requestInfo.RequestID,
			"error":      err.Error(),
		}).Error("Failed to finalize request logging")
	}
	}
}

// captureRequestInfo extracts relevant information from the incoming HTTP request.
// It captures the URL, method, headers, and body. The request body is read and then
// restored so that it can be processed by subsequent handlers.
func captureRequestInfo(c *gin.Context) (*RequestInfo, error) {
	// Capture URL with sensitive query parameters masked
	maskedQuery := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
	url := c.Request.URL.Path
	if maskedQuery != "" {
		url += "?" + maskedQuery
	}

	// Capture method
	method := c.Request.Method

	// Capture headers
	headers := make(map[string][]string)
	for key, values := range c.Request.Header {
		headers[key] = values
	}

	// Capture request body
	var body []byte
	if c.Request.Body != nil {
		// Read the body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}

		// Restore the body for the actual request processing
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		body = bodyBytes
	}

	return &RequestInfo{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		RequestID: logging.GetGinRequestID(c),
		Timestamp: time.Now(),
	}, nil
}

// shouldLogRequest determines whether the request should be logged.
// It skips management endpoints to avoid leaking secrets but allows
// all other routes, including module-provided ones, to honor request-log.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	if strings.HasPrefix(path, "/api") {
		return strings.HasPrefix(path, "/api/provider")
	}

	return true
}
