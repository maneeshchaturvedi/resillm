package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/resillm/resillm/internal/types"
)

// Buffer size constants for streaming
const (
	// DefaultStreamBufferSize is the default buffer size for streaming channels.
	// This prevents goroutine accumulation when consumers are slow.
	DefaultStreamBufferSize = 32

	// DefaultScannerInitialBuf is the initial buffer size for the scanner (64KB).
	DefaultScannerInitialBuf = 64 * 1024

	// DefaultScannerMaxBuf is the maximum buffer size for the scanner (512KB).
	// This bounds memory usage: 1000 concurrent requests × 512KB = 512MB worst case.
	DefaultScannerMaxBuf = 512 * 1024
)

// scannerBufferPool provides reusable buffers for streaming to reduce GC pressure.
// Each buffer starts at 64KB and can grow up to 512KB max.
var scannerBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, DefaultScannerInitialBuf)
		return &buf
	},
}

// getBuffer retrieves a buffer from the pool.
func getBuffer() []byte {
	bufPtr := scannerBufferPool.Get().(*[]byte)
	buf := *bufPtr
	return buf[:0] // Reset length but keep capacity
}

// putBuffer returns a buffer to the pool.
// Only returns buffers that haven't grown too large to prevent memory bloat.
func putBuffer(buf []byte) {
	// Don't pool buffers that have grown beyond 2x the initial size
	// to prevent memory bloat from outlier large responses
	if cap(buf) <= DefaultScannerInitialBuf*2 {
		scannerBufferPool.Put(&buf)
	}
}

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

// StreamConfig holds configuration for stream reading.
type StreamConfig struct {
	ChannelBufferSize int // Buffer size for the chunk channel
	ScannerInitialBuf int // Initial scanner buffer size
	ScannerMaxBuf     int // Maximum scanner buffer size (bounds memory usage)
}

// DefaultStreamConfig returns the default stream configuration.
func DefaultStreamConfig() StreamConfig {
	return StreamConfig{
		ChannelBufferSize: DefaultStreamBufferSize,
		ScannerInitialBuf: DefaultScannerInitialBuf,
		ScannerMaxBuf:     DefaultScannerMaxBuf,
	}
}

// BoundedStreamReader handles reading and parsing SSE streams with bounded memory usage.
type BoundedStreamReader struct {
	body      io.ReadCloser
	parser    SSEParser
	converter ChunkConverter
	model     string
	config    StreamConfig
}

// NewBoundedStreamReader creates a new BoundedStreamReader with memory-safe defaults.
func NewBoundedStreamReader(body io.ReadCloser, parser SSEParser, converter ChunkConverter, model string, config StreamConfig) *BoundedStreamReader {
	if config.ChannelBufferSize <= 0 {
		config.ChannelBufferSize = DefaultStreamBufferSize
	}
	if config.ScannerInitialBuf <= 0 {
		config.ScannerInitialBuf = DefaultScannerInitialBuf
	}
	if config.ScannerMaxBuf <= 0 {
		config.ScannerMaxBuf = DefaultScannerMaxBuf
	}
	return &BoundedStreamReader{
		body:      body,
		parser:    parser,
		converter: converter,
		model:     model,
		config:    config,
	}
}

// ReadStream reads the SSE stream and sends chunks to a channel.
// The returned channel is buffered to prevent goroutine blocking.
// Memory usage is bounded by ScannerMaxBuf per stream.
func (sr *BoundedStreamReader) ReadStream() <-chan types.StreamChunk {
	chunkChan := make(chan types.StreamChunk, sr.config.ChannelBufferSize)

	go func() {
		defer close(chunkChan)
		defer sr.body.Close()

		// Get a buffer from the pool
		buf := getBuffer()
		defer putBuffer(buf)

		scanner := bufio.NewScanner(sr.body)
		// Set bounded buffer: starts at initial size, max growth to ScannerMaxBuf
		scanner.Buffer(buf, sr.config.ScannerMaxBuf)

		for scanner.Scan() {
			line := scanner.Text()

			data, done, err := sr.parser.ParseLine(line)
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

		if err := scanner.Err(); err != nil {
			// Check if it's a buffer overflow error
			if err == bufio.ErrTooLong {
				chunkChan <- types.StreamChunk{Error: fmt.Errorf("stream line exceeded maximum size of %d bytes", sr.config.ScannerMaxBuf)}
			} else {
				chunkChan <- types.StreamChunk{Error: err}
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

// Legacy StreamReader for backward compatibility - uses unbounded bufio.Reader.
// Deprecated: Use BoundedStreamReader instead for memory-safe streaming.
type StreamReader struct {
	reader     *bufio.Reader
	parser     SSEParser
	converter  ChunkConverter
	model      string
	bufferSize int
}

// NewStreamReader creates a new StreamReader.
// Deprecated: Use NewBoundedStreamReader instead for memory-safe streaming.
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
// Deprecated: This method uses unbounded memory. Use BoundedStreamReader.ReadStream() instead.
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
