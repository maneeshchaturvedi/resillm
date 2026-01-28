package server

import (
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// LoadShedder provides load shedding and connection limiting to protect
// the server from overload conditions.
type LoadShedder struct {
	maxActiveRequests   int64
	maxConnectionsPerIP int64
	activeRequests      atomic.Int64
	connectionsPerIP    map[string]*atomic.Int64
	connMu              sync.RWMutex

	// Metrics
	totalShed     atomic.Int64
	totalAccepted atomic.Int64
}

// NewLoadShedder creates a new load shedder.
// maxActive: maximum concurrent requests (0 = unlimited)
// maxPerIP: maximum connections per IP (0 = unlimited)
func NewLoadShedder(maxActive, maxPerIP int64) *LoadShedder {
	return &LoadShedder{
		maxActiveRequests:   maxActive,
		maxConnectionsPerIP: maxPerIP,
		connectionsPerIP:    make(map[string]*atomic.Int64),
	}
}

// Middleware returns an HTTP middleware that implements load shedding.
func (ls *LoadShedder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if we should shed this request
		if ls.shouldShed() {
			ls.totalShed.Add(1)
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"type":"overloaded","message":"Server is overloaded, please retry"}}`, http.StatusServiceUnavailable)
			return
		}

		// Track active request
		ls.activeRequests.Add(1)
		ls.totalAccepted.Add(1)
		defer ls.activeRequests.Add(-1)

		next.ServeHTTP(w, r)
	})
}

// shouldShed returns true if the request should be rejected.
func (ls *LoadShedder) shouldShed() bool {
	if ls.maxActiveRequests <= 0 {
		return false
	}
	return ls.activeRequests.Load() >= ls.maxActiveRequests
}

// TrackConnection tracks a new connection from an IP.
// Returns false if the connection should be rejected (too many from this IP).
func (ls *LoadShedder) TrackConnection(ip string) bool {
	if ls.maxConnectionsPerIP <= 0 {
		return true // No limit
	}

	ls.connMu.RLock()
	counter, exists := ls.connectionsPerIP[ip]
	ls.connMu.RUnlock()

	if !exists {
		ls.connMu.Lock()
		// Double-check
		if counter, exists = ls.connectionsPerIP[ip]; !exists {
			counter = &atomic.Int64{}
			ls.connectionsPerIP[ip] = counter
		}
		ls.connMu.Unlock()
	}

	// Check limit before incrementing
	current := counter.Load()
	if current >= ls.maxConnectionsPerIP {
		return false
	}

	counter.Add(1)
	return true
}

// ReleaseConnection releases a connection from an IP.
func (ls *LoadShedder) ReleaseConnection(ip string) {
	ls.connMu.RLock()
	counter, exists := ls.connectionsPerIP[ip]
	ls.connMu.RUnlock()

	if exists {
		counter.Add(-1)
	}
}

// ConnStateHandler returns a connection state handler for http.Server.
// This tracks connections per IP for limiting.
func (ls *LoadShedder) ConnStateHandler(conn net.Conn, state http.ConnState) {
	if ls.maxConnectionsPerIP <= 0 {
		return // No per-IP limiting
	}

	ip := getIPFromConn(conn)
	if ip == "" {
		return
	}

	switch state {
	case http.StateNew:
		if !ls.TrackConnection(ip) {
			// Too many connections from this IP - close it
			log.Debug().
				Str("ip", ip).
				Int64("max", ls.maxConnectionsPerIP).
				Msg("Rejecting connection: too many from this IP")
			conn.Close()
		}
	case http.StateClosed, http.StateHijacked:
		ls.ReleaseConnection(ip)
	}
}

// getIPFromConn extracts the IP address from a connection.
func getIPFromConn(conn net.Conn) string {
	addr := conn.RemoteAddr()
	if addr == nil {
		return ""
	}

	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// Stats returns load shedder statistics.
func (ls *LoadShedder) Stats() LoadShedderStats {
	return LoadShedderStats{
		ActiveRequests:      ls.activeRequests.Load(),
		MaxActiveRequests:   ls.maxActiveRequests,
		MaxConnectionsPerIP: ls.maxConnectionsPerIP,
		TotalShed:           ls.totalShed.Load(),
		TotalAccepted:       ls.totalAccepted.Load(),
	}
}

// LoadShedderStats holds load shedder statistics.
type LoadShedderStats struct {
	ActiveRequests      int64 `json:"active_requests"`
	MaxActiveRequests   int64 `json:"max_active_requests"`
	MaxConnectionsPerIP int64 `json:"max_connections_per_ip"`
	TotalShed           int64 `json:"total_shed"`
	TotalAccepted       int64 `json:"total_accepted"`
}
