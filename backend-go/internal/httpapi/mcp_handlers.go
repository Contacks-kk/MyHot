package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const mcpMaxBody = 256 * 1024

var mcpAuthorityPattern = regexp.MustCompile(`^(\[[0-9a-f:.]+\]|[a-z0-9._-]+)(?::([0-9]+))?$`)

func (s *Server) mcpEndpoint() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			s.mcpPreflight(w, r)
			return
		}
		setPrivateSiteHeaders(w)
		if !s.requireSiteUser(w, r) {
			return
		}
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
			writeMCPError(w, http.StatusMethodNotAllowed, -32000, "Method not allowed")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete && r.Method != http.MethodHead {
			w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
			writeMCPError(w, http.StatusMethodNotAllowed, -32000, "Method not allowed")
			return
		}
		if !s.mcpAllowedHost(r) {
			writeMCPJSON(w, http.StatusMisdirectedRequest, map[string]string{"error": "misdirected_request"})
			return
		}
		origin := r.Header.Get("Origin")
		if !s.mcpAllowedOrigin(origin) {
			writeMCPJSON(w, http.StatusForbidden, map[string]string{"error": "origin_not_allowed"})
			return
		}
		setMCPCORS(w, origin)
		if r.Method == http.MethodPost {
			body, tooLarge, err := readMCPBody(w, r)
			if tooLarge {
				writeMCPError(w, http.StatusRequestEntityTooLarge, -32000, "MCP request body is too large.")
				return
			}
			if err != nil {
				writeMCPError(w, http.StatusBadRequest, -32700, "Parse error")
				return
			}
			if isMCPBatch(body) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Batch requests are not supported"},"id":null}`)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		if s.MCPHandler == nil {
			writeMCPError(w, http.StatusServiceUnavailable, -32603, "Internal error")
			return
		}
		s.MCPHandler.ServeHTTP(mcpResponseWriter{ResponseWriter: w}, r)
	})
}

func isMCPBatch(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var batch []json.RawMessage
	return json.Unmarshal(trimmed, &batch) == nil
}

func (s *Server) mcpPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if !s.mcpAllowedOrigin(origin) {
		writeMCPJSON(w, http.StatusForbidden, map[string]string{"error": "origin_not_allowed"})
		return
	}
	setMCPCORS(w, origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, MCP-Protocol-Version, MCP-Session-Id, Last-Event-ID, MCP-Method, MCP-Name")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
	w.WriteHeader(http.StatusNoContent)
}

func setMCPCORS(w http.ResponseWriter, origin string) {
	addVaryOrigin(w.Header())
	if origin == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Expose-Headers", "MCP-Protocol-Version, MCP-Session-Id, Link")
}

func readMCPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool, error) {
	if r.ContentLength > mcpMaxBody {
		return nil, true, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, mcpMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			return nil, true, nil
		}
		return nil, false, err
	}
	return body, false, nil
}

func (s *Server) mcpAllowedHost(r *http.Request) bool {
	authority := r.Host
	if values, exists := r.Header[http.CanonicalHeaderKey("X-Forwarded-Host")]; exists {
		if len(values) != 1 {
			return false
		}
		authority = values[0]
	}
	host := mcpHostname(authority)
	if host == "" {
		return false
	}
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "[::1]": true}
	if address, err := url.Parse(s.Config.SiteURL); err == nil {
		allowed[mcpNormalizeHostname(address.Hostname())] = true
	}
	for _, configured := range s.Config.MCPAllowedHosts {
		if configured = strings.TrimSpace(configured); configured != "" {
			allowed[mcpHostname(configured)] = true
		}
	}
	return allowed[host]
}

func mcpHostname(authority string) string {
	match := mcpAuthorityPattern.FindStringSubmatch(authority)
	if match == nil || match[0] != authority {
		return ""
	}
	if match[2] != "" {
		port, err := strconv.Atoi(match[2])
		if err != nil || port > 65535 {
			return ""
		}
	}
	host := strings.ToLower(match[1])
	if strings.HasPrefix(host, "[") {
		parsed := net.ParseIP(strings.Trim(host, "[]"))
		if parsed == nil || parsed.To4() != nil {
			return ""
		}
		return "[" + parsed.String() + "]"
	}
	return host
}

func mcpNormalizeHostname(host string) string {
	trimmed := strings.Trim(host, "[]")
	if address := net.ParseIP(trimmed); address != nil {
		if address.To4() != nil {
			return address.To4().String()
		}
		return "[" + address.String() + "]"
	}
	if strings.Contains(host, ":") {
		return "[" + strings.ToLower(trimmed) + "]"
	}
	return strings.ToLower(host)
}

func (s *Server) mcpAllowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := mcpNormalizeHostname(parsed.Hostname())
	if site, err := url.Parse(s.Config.SiteURL); err == nil && parsed.Scheme == site.Scheme && host == mcpNormalizeHostname(site.Hostname()) {
		return true
	}
	if (host == "localhost" || host == "127.0.0.1") && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return true
	}
	return false
}

func writeMCPError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ID any `json:"id"`
	}{JSONRPC: "2.0", Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
	_, _ = w.Write(body)
}

func writeMCPJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body, _ := json.Marshal(value)
	_, _ = w.Write(body)
}

type mcpResponseWriter struct {
	http.ResponseWriter
}

func (w mcpResponseWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
	addVaryOrigin(w.Header())
	w.ResponseWriter.WriteHeader(status)
}

func (w mcpResponseWriter) Write(body []byte) (int, error) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Surrogate-Control", "no-store")
	addVaryOrigin(w.Header())
	return w.ResponseWriter.Write(body)
}

func addVaryOrigin(headers http.Header) {
	for _, value := range headers.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(name), "Origin") {
				return
			}
		}
	}
	headers.Add("Vary", "Origin")
}
