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
	openai "github.com/sashabaranov/go-openai"
)

// Request validation limits
const (
	MaxMessageSize = 32 * 1024       // 32KB per message
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
		if err.Error() == "http: request body too large" {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("Request body exceeds maximum size of %d bytes", MaxBodySize))
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req openai.ChatCompletionRequest
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

	// Debug log the incoming request
	if s.cfg.Logging.LogRequests {
		log.Debug().
			Str("request_id", requestID).
			Str("model", req.Model).
			Bool("stream", req.Stream).
			Int("message_count", len(req.Messages)).
			RawJSON("request_body", body).
			Msg("Incoming chat completion request")
	}

	// Check budget before proceeding
	if s.budget != nil {
		budgetCheck := s.budget.Check(0)
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
		s.handleStreamingChat(w, r, req, requestID)
		return
	}

	// Execute via router
	start := time.Now()
	resp, meta, err := s.router.ExecuteChat(ctx, req)
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

	// Audit logging
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

	// Set response headers with metadata
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("X-Resillm-Provider", meta.Provider)
	w.Header().Set("X-Resillm-Model", meta.ActualModel)
	w.Header().Set("X-Resillm-Latency-Ms", formatInt(latency.Milliseconds()))
	w.Header().Set("X-Resillm-Cost", fmt.Sprintf("%.6f", meta.Cost))
	w.Header().Set("X-Resillm-Retries", strconv.Itoa(meta.Retries))
	w.Header().Set("X-Resillm-Fallback", strconv.FormatBool(meta.Fallback))

	// Debug log the response
	if s.cfg.Logging.LogResponses {
		respBytes, _ := json.Marshal(resp)
		log.Debug().
			Str("request_id", requestID).
			RawJSON("response_body", respBytes).
			Msg("Chat completion response")
	}

	// Write response directly - no wrapper needed
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode response")
	}
}

// handleStreamingChat handles streaming chat completions
func (s *Server) handleStreamingChat(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest, requestID string) {
	ctx := r.Context()

	log.Debug().
		Str("request_id", requestID).
		Str("model", req.Model).
		Msg("Starting streaming chat completion")

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
	streamResult, meta, err := s.router.ExecuteChatStream(ctx, req)
	if err != nil {
		writeSSEError(w, flusher, err.Error())
		writeSSEDone(w, flusher)
		return
	}

	// Defensive nil check - should never happen but prevents panic
	if streamResult == nil || streamResult.Stream == nil {
		log.Error().Str("request_id", requestID).Msg("Stream result is nil")
		writeSSEError(w, flusher, "internal error: stream initialization failed")
		writeSSEDone(w, flusher)
		return
	}

	// Ensure cleanup when done - safe to call multiple times
	defer func() {
		if streamResult.Stream != nil {
			streamResult.Stream.Close()
		}
		if streamResult.Sem != nil {
			streamResult.Sem.Release()
		}
	}()

	// Set provider header after we know which one was used
	w.Header().Set("X-Resillm-Provider", meta.Provider)
	w.Header().Set("X-Resillm-Model", meta.ActualModel)
	w.Header().Set("X-Resillm-Fallback", strconv.FormatBool(meta.Fallback))

	// Stream chunks to client
	// Use a done channel to coordinate context cancellation with blocking Recv()
	for {
		// Check context before blocking on Recv
		select {
		case <-ctx.Done():
			// Client disconnected or timeout - still send [DONE]
			log.Debug().Str("request_id", requestID).Msg("Client disconnected")
			writeSSEDone(w, flusher)
			return
		default:
		}

		// Recv() blocks until a chunk is available or stream ends
		chunk, err := streamResult.Stream.Recv()
		if errors.Is(err, io.EOF) {
			log.Debug().
				Str("request_id", requestID).
				Msg("Streaming completed")
			writeSSEDone(w, flusher)
			return
		}
		if err != nil {
			log.Error().
				Str("request_id", requestID).
				Err(err).
				Msg("Streaming error")
			writeSSEError(w, flusher, sanitizeError(err))
			writeSSEDone(w, flusher)
			return
		}

		data, err := json.Marshal(chunk)
		if err != nil {
			log.Error().Err(err).Str("request_id", requestID).Msg("Failed to marshal stream chunk - terminating stream")
			writeSSEError(w, flusher, "internal error: failed to encode response")
			writeSSEDone(w, flusher)
			return
		}

		// Debug log each chunk if response logging is enabled
		if s.cfg.Logging.LogResponses {
			log.Debug().
				Str("request_id", requestID).
				RawJSON("chunk", data).
				Msg("Streaming chunk")
		}

		w.Write([]byte("data: "))
		w.Write(data)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}
}

// handleCompletions handles POST /v1/completions (legacy)
func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, http.StatusNotImplemented, "not_implemented", "Legacy completions API not yet supported")
}

// handleEmbeddings handles POST /v1/embeddings
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, http.StatusNotImplemented, "not_implemented", "Embeddings API not yet supported")
}

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	httpStatus := http.StatusOK

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

// handleModels handles GET /v1/models
// Returns a list of configured models in OpenAI-compatible format
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type ModelInfo struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	type ModelsResponse struct {
		Object string      `json:"object"`
		Data   []ModelInfo `json:"data"`
	}

	// Build list from configured models
	var models []ModelInfo
	for name := range s.cfg.Models {
		models = append(models, ModelInfo{
			ID:      name,
			Object:  "model",
			Created: startTime.Unix(),
			OwnedBy: "resillm",
		})
	}

	resp := ModelsResponse{
		Object: "list",
		Data:   models,
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
			"alert_triggered":    status.AlertTriggered,
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

func writeSSEDone(w http.ResponseWriter, flusher http.Flusher) {
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func generateRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(b)
}

func formatInt(i int64) string {
	return strconv.FormatInt(i, 10)
}

// validateRequest performs input validation on the request
func validateRequest(req *openai.ChatCompletionRequest) error {
	if req.Model == "" {
		return errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return errors.New("messages is required")
	}
	if len(req.Messages) > MaxMessages {
		return fmt.Errorf("too many messages: %d (max %d)", len(req.Messages), MaxMessages)
	}
	if req.MaxTokens > MaxTokensLimit {
		return fmt.Errorf("max_tokens too large: %d (max %d)", req.MaxTokens, MaxTokensLimit)
	}
	if req.Temperature < 0 || req.Temperature > 2 {
		return errors.New("temperature must be between 0 and 2")
	}
	if req.TopP < 0 || req.TopP > 1 {
		return errors.New("top_p must be between 0 and 1")
	}
	for i, msg := range req.Messages {
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

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = openaiKeyPattern.ReplaceAllString(msg, "[REDACTED]")
	msg = anthropicKeyPattern.ReplaceAllString(msg, "[REDACTED]")
	return msg
}

var startTime = time.Now()
