package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/resillm/resillm/internal/types"
)

// DefaultStreamBufferSize is the default buffer size for streaming channels.
// This prevents goroutine accumulation when consumers are slow.
const DefaultStreamBufferSize = 32

// SSEParser defines how to parse Server-Sent Events for a specific provider.
type SSEParser interface {
	// ParseLine parses a single SSE line and returns:
	// - data: the parsed data (nil to skip this line)
	// - done: true if this is the end of the stream
	// - err: any parsing error
	ParseLine(line string) (data []byte, done bool, err error)
}

// ChunkConverter converts provider-specific data to OpenAI-compatible chunks.
type ChunkConverter interface {
	// ConvertToChunk converts parsed data to a ChatCompletionChunk.
	// Returns nil to skip this chunk.
	ConvertToChunk(data []byte, model string) (*types.ChatCompletionChunk, error)
}

// StreamReader handles reading and parsing SSE streams.
type StreamReader struct {
	reader     *bufio.Reader
	parser     SSEParser
	converter  ChunkConverter
	model      string
	bufferSize int
}

// NewStreamReader creates a new StreamReader.
func NewStreamReader(body io.Reader, parser SSEParser, converter ChunkConverter, model string, bufferSize int) *StreamReader {
	if bufferSize <= 0 {
		bufferSize = DefaultStreamBufferSize
	}
	return &StreamReader{
		reader:     bufio.NewReader(body),
		parser:     parser,
		converter:  converter,
		model:      model,
		bufferSize: bufferSize,
	}
}

// ReadStream reads the SSE stream and sends chunks to a channel.
// The returned channel is buffered to prevent goroutine blocking.
func (sr *StreamReader) ReadStream() <-chan types.StreamChunk {
	chunkChan := make(chan types.StreamChunk, sr.bufferSize)

	go func() {
		defer close(chunkChan)

		for {
			line, err := sr.reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					chunkChan <- types.StreamChunk{Error: err}
				}
				return
			}

			data, done, err := sr.parser.ParseLine(string(line))
			if err != nil {
				chunkChan <- types.StreamChunk{Error: fmt.Errorf("parsing SSE line: %w", err)}
				return
			}

			if done {
				return
			}

			if data == nil {
				continue // Skip this line
			}

			chunk, err := sr.converter.ConvertToChunk(data, sr.model)
			if err != nil {
				chunkChan <- types.StreamChunk{Error: fmt.Errorf("converting chunk: %w", err)}
				return
			}

			if chunk != nil {
				chunkChan <- types.StreamChunk{Data: chunk}
			}
		}
	}()

	return chunkChan
}

// OpenAISSEParser parses OpenAI-style SSE events (data: prefix with [DONE] terminator).
type OpenAISSEParser struct{}

func (p *OpenAISSEParser) ParseLine(line string) ([]byte, bool, error) {
	lineStr := strings.TrimSpace(line)

	// Skip empty lines
	if lineStr == "" {
		return nil, false, nil
	}

	// Skip non-data lines
	if !strings.HasPrefix(lineStr, "data: ") {
		return nil, false, nil
	}

	data := strings.TrimPrefix(lineStr, "data: ")

	// Check for done marker
	if data == "[DONE]" {
		return nil, true, nil
	}

	return []byte(data), false, nil
}

// OpenAIChunkConverter converts OpenAI SSE data to chunks.
type OpenAIChunkConverter struct{}

func (c *OpenAIChunkConverter) ConvertToChunk(data []byte, model string) (*types.ChatCompletionChunk, error) {
	var chunk types.ChatCompletionChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, err
	}
	return &chunk, nil
}

// AnthropicSSEParser parses Anthropic-style SSE events.
type AnthropicSSEParser struct{}

func (p *AnthropicSSEParser) ParseLine(line string) ([]byte, bool, error) {
	lineStr := strings.TrimSpace(line)

	if lineStr == "" || !strings.HasPrefix(lineStr, "data: ") {
		return nil, false, nil
	}

	data := strings.TrimPrefix(lineStr, "data: ")
	return []byte(data), false, nil
}

// OllamaSSEParser parses Ollama-style streaming (JSON per line, no SSE prefix).
type OllamaSSEParser struct{}

func (p *OllamaSSEParser) ParseLine(line string) ([]byte, bool, error) {
	lineStr := strings.TrimSpace(line)
	if lineStr == "" {
		return nil, false, nil
	}
	return []byte(lineStr), false, nil
}
