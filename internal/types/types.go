package types

// ExecutionMeta contains metadata about request execution (internal use)
type ExecutionMeta struct {
	Provider    string
	ActualModel string
	Retries     int
	Fallback    bool
	Cost        float64
}

// ErrorResponse represents an OpenAI-compatible error response
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error details
type ErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
