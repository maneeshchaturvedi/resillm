package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/resillm/resillm/internal/types"
	"github.com/rs/zerolog/log"
)

// Request validation limits
const (
	MaxMessageSize = 32 * 1024      // 32KB per message
	MaxMessages    = 100
	MaxTokensLimit = 100000
	MaxBodySize    = 5 * 1024 * 1024 // 5MB hard limit for request body
)

// Regex patterns for sanitizing sensitive data
var (
	openaiKeyPattern    = regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`)
	anthropicKeyPattern = regexp.MustCompile(`sk-ant-[a-zA-Z0-9-]+`)
)

// handleChatCompletions handles POST /v1/chat/completions
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Enforce body size limit to prevent DoS attacks
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodySize)

	// Parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Check if error is due to body size exceeded
		if err.Error() == "http: request body too large" {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("Request body exceeds maximum size of %d bytes", MaxBodySize))
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req types.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON: "+err.Error())
		return
	}

	// Validate request
	if err := validateRequest(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Extract request ID from headers if present
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = generateRequestID()
	}

	// Check budget before proceeding
	if s.budget != nil {
		budgetCheck := s.budget.Check(0) // Check with zero estimated cost for now
		if !budgetCheck.Allowed {
			s.writeError(w, http.StatusTooManyRequests, "budget_exceeded", budgetCheck.Message)
			return
		}
		if budgetCheck.Warning {
			w.Header().Set("X-Resillm-Budget-Warning", budgetCheck.Message)
		}
		if budgetCheck.AlertTriggered {
			log.Warn().
				Float64("hourly_remaining", budgetCheck.RemainingHour).
				Float64("daily_remaining", budgetCheck.RemainingDay).
				Msg("Budget alert threshold reached")
		}
	}

	// Check if streaming
	if req.Stream {
		s.handleStreamingChat(w, r, &req, requestID)
		return
	}

	// Execute via router
	start := time.Now()
	resp, meta, err := s.router.ExecuteChat(ctx, &req)
	latency := time.Since(start)

	if err != nil {
		log.Error().Err(err).Str("model", req.Model).Msg("Chat completion failed")
		s.writeError(w, http.StatusBadGateway, "provider_error", sanitizeError(err))
		return
	}

	// Record cost in budget tracker
	if s.budget != nil && meta.Cost > 0 {
		s.budget.Record(meta.Cost)
	}

	// Add resillm metadata to response
	resp.ResillmMeta = &types.ResillmMeta{
		Provider:       meta.Provider,
		ActualModel:    meta.ActualModel,
		RequestedModel: req.Model,
		LatencyMs:      latency.Milliseconds(),
		Retries:        meta.Retries,
		Fallback:       meta.Fallback,
		CostUSD:        meta.Cost,
		RequestID:      requestID,
	}

	// Audit logging - log model, provider, cost, tokens per request
	log.Info().
		Str("request_id", requestID).
		Str("model", req.Model).
		Str("actual_model", meta.ActualModel).
		Str("provider", meta.Provider).
		Float64("cost_usd", meta.Cost).
		Int("prompt_tokens", resp.Usage.PromptTokens).
		Int("completion_tokens", resp.Usage.CompletionTokens).
		Int("total_tokens", resp.Usage.TotalTokens).
		Int("retries", meta.Retries).
		Bool("fallback", meta.Fallback).
		Int64("latency_ms", latency.Milliseconds()).
		Msg("Chat completion succeeded")

	// Set response headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("X-Resillm-Provider", meta.Provider)
	w.Header().Set("X-Resillm-Latency-Ms", formatInt(latency.Milliseconds()))

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode response")
	}
}

// handleStreamingChat handles streaming chat completions
func (s *Server) handleStreamingChat(w http.ResponseWriter, r *http.Request, req *types.ChatCompletionRequest, requestID string) {
	ctx := r.Context()

	// Set streaming headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-ID", requestID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming_error", "Streaming not supported")
		return
	}

	// Execute streaming via router
	streamChan, meta, err := s.router.ExecuteChatStream(ctx, req)
	if err != nil {
		// Write error as SSE
		writeSSEError(w, flusher, err.Error())
		return
	}

	// Set provider header after we know which one was used
	w.Header().Set("X-Resillm-Provider", meta.Provider)

	// Stream chunks to client with context cancellation support
	for {
		select {
		case <-ctx.Done():
			// Client disconnected or timeout
			return
		case chunk, ok := <-streamChan:
			if !ok {
				// Stream complete
				w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
				return
			}

			if chunk.Error != nil {
				writeSSEError(w, flusher, sanitizeError(chunk.Error))
				return
			}

			data, err := json.Marshal(chunk.Data)
			if err != nil {
				log.Error().Err(err).Msg("Failed to marshal stream chunk")
				continue
			}

			w.Write([]byte("data: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

// handleCompletions handles POST /v1/completions (legacy)
func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	// For now, return not implemented
	s.writeError(w, http.StatusNotImplemented, "not_implemented", "Legacy completions API not yet supported")
}

// handleEmbeddings handles POST /v1/embeddings
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	// For now, return not implemented
	s.writeError(w, http.StatusNotImplemented, "not_implemented", "Embeddings API not yet supported")
}

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	httpStatus := http.StatusOK

	// Check if any provider is available (circuit not open)
	providers := s.router.GetProviderStatus()
	allCircuitsOpen := true
	for _, p := range providers {
		if p.Circuit == "closed" || p.Circuit == "half-open" || p.Circuit == "" {
			allCircuitsOpen = false
			break
		}
	}

	if allCircuitsOpen && len(providers) > 0 {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	resp := map[string]interface{}{
		"status":    status,
		"uptime":    time.Since(startTime).String(),
		"providers": providers,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode health response")
	}
}

// handleProviders handles GET /v1/providers
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	providers := s.router.GetProviderStatus()

	resp := map[string]interface{}{
		"providers": providers,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleBudget handles GET /v1/budget
func (s *Server) handleBudget(w http.ResponseWriter, r *http.Request) {
	var resp map[string]interface{}

	if s.budget != nil {
		status := s.budget.Status()
		resp = map[string]interface{}{
			"enabled": s.cfg.Budget.Enabled,
			"current_hour": map[string]interface{}{
				"spent":     status.HourlySpent,
				"limit":     status.HourlyLimit,
				"remaining": status.HourlyRemaining,
				"percent":   status.HourlyPercent,
			},
			"current_day": map[string]interface{}{
				"spent":     status.DailySpent,
				"limit":     status.DailyLimit,
				"remaining": status.DailyRemaining,
				"percent":   status.DailyPercent,
			},
			"alert_triggered":   status.AlertTriggered,
			"action_on_exceeded": s.cfg.Budget.ActionOnExceeded,
		}
	} else {
		resp = map[string]interface{}{
			"enabled": false,
			"current_hour": map[string]interface{}{
				"spent":     0.0,
				"limit":     s.cfg.Budget.MaxCostPerHour,
				"remaining": s.cfg.Budget.MaxCostPerHour,
			},
			"current_day": map[string]interface{}{
				"spent":     0.0,
				"limit":     s.cfg.Budget.MaxCostPerDay,
				"remaining": s.cfg.Budget.MaxCostPerDay,
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleReload handles POST /admin/reload
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	err := s.ReloadConfig()
	if err != nil {
		log.Error().Err(err).Msg("Config reload failed")
		s.writeError(w, http.StatusInternalServerError, "reload_failed", err.Error())
		return
	}

	resp := map[string]interface{}{
		"status":  "reloaded",
		"message": "Configuration reloaded successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Helper functions

func (s *Server) writeError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	resp := types.ErrorResponse{
		Error: types.ErrorDetail{
			Type:    errType,
			Message: message,
		},
	}

	json.NewEncoder(w).Encode(resp)
}

func writeSSEError(w http.ResponseWriter, flusher http.Flusher, message string) {
	errResp := map[string]interface{}{
		"error": map[string]string{
			"message": message,
		},
	}
	data, err := json.Marshal(errResp)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal SSE error")
		return
	}
	w.Write([]byte("data: "))
	w.Write(data)
	w.Write([]byte("\n\n"))
	flusher.Flush()
}

// generateRequestID creates a cryptographically secure request ID
func generateRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (extremely rare)
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(b)
}

// formatInt converts int64 to string representation
func formatInt(i int64) string {
	return strconv.FormatInt(i, 10)
}

// validateRequest performs comprehensive input validation
func validateRequest(req *types.ChatCompletionRequest) error {
	if req.Model == "" {
		return errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages is required")
	}
	if len(req.Messages) > MaxMessages {
		return fmt.Errorf("too many messages: %d (max %d)", len(req.Messages), MaxMessages)
	}
	if req.MaxTokens != nil && *req.MaxTokens > MaxTokensLimit {
		return fmt.Errorf("max_tokens too large: %d (max %d)", *req.MaxTokens, MaxTokensLimit)
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return errors.New("top_p must be between 0 and 1")
	}
	for i, msg := range req.Messages {
		// Check content size - handle both string and other types
		if contentStr, ok := msg.Content.(string); ok {
			if len(contentStr) > MaxMessageSize {
				return fmt.Errorf("message %d too large: %d bytes (max %d)", i, len(contentStr), MaxMessageSize)
			}
		}
		// Validate role
		switch msg.Role {
		case "system", "user", "assistant", "tool":
			// Valid roles
		default:
			return fmt.Errorf("invalid role in message %d: %s", i, msg.Role)
		}
	}
	return nil
}

// sanitizeError removes sensitive information (API keys) from error messages
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Remove OpenAI API key patterns
	msg = openaiKeyPattern.ReplaceAllString(msg, "[REDACTED]")
	// Remove Anthropic API key patterns
	msg = anthropicKeyPattern.ReplaceAllString(msg, "[REDACTED]")
	return msg
}

var startTime = time.Now()
