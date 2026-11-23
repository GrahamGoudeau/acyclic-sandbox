// Package proxy forwards the sandbox's model calls to an OpenAI-compatible
// API (OpenRouter by default) without exposing the key. It accepts only
// requests shaped like the SDK's agent loop, pins the model, caps tokens,
// and rate limits per client IP and globally per day.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Upstream     string         // e.g. https://openrouter.ai/api/v1
	APIKey       string
	Model        string         // pinned; the client's model field is ignored
	ExtraBody    map[string]any // merged into every request (provider routing)
	MaxTokens    int            // per call
	PerIPWindow  int            // calls per 10 minutes per IP
	PerIPDay     int
	GlobalDay    int
	AllowedTools map[string]bool
	TrustProxy   bool // read the client IP from X-Forwarded-For (behind Caddy)
}

type Proxy struct {
	cfg    Config
	client *http.Client
	mu     sync.Mutex
	day    string
	global int
	ips    map[string]*bucket
}

type bucket struct {
	recent []time.Time
	day    int
}

func New(cfg Config) *Proxy {
	return &Proxy{cfg: cfg, client: &http.Client{Timeout: 90 * time.Second}, ips: map[string]*bucket{}}
}

func (p *Proxy) clientIP(r *http.Request) string {
	if p.cfg.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

// allow applies the three limits and counts the call.
func (p *Proxy) allow(ip string) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if today != p.day {
		p.day, p.global, p.ips = today, 0, map[string]*bucket{}
	}
	if p.global >= p.cfg.GlobalDay {
		return false, "the sandbox hit its daily model budget; try again tomorrow or run the SDK with your own key"
	}
	b := p.ips[ip]
	if b == nil {
		b = &bucket{}
		p.ips[ip] = b
	}
	cut := time.Now().Add(-10 * time.Minute)
	keep := b.recent[:0]
	for _, t := range b.recent {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	b.recent = keep
	if len(b.recent) >= p.cfg.PerIPWindow {
		return false, "too many model calls from this address; wait a few minutes"
	}
	if b.day >= p.cfg.PerIPDay {
		return false, "daily limit for this address reached"
	}
	b.recent = append(b.recent, time.Now())
	b.day++
	p.global++
	return true, ""
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
}

// ServeHTTP handles POST .../chat/completions.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST only")
		return
	}
	if p.cfg.APIKey == "" {
		fail(w, 503, "model proxy is not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil || len(body) >= 256<<10 {
		fail(w, 413, "request too large")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		fail(w, 400, "bad JSON")
		return
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 || len(msgs) > 80 {
		fail(w, 400, "1 to 80 messages")
		return
	}
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		fn, _ := t.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if !p.cfg.AllowedTools[name] {
			fail(w, 400, "only the SDK's EHR tools are allowed through this proxy")
			return
		}
	}
	if ok, why := p.allow(p.clientIP(r)); !ok {
		w.Header().Set("Retry-After", "300")
		fail(w, 429, why)
		return
	}
	out := map[string]any{"model": p.cfg.Model, "messages": msgs, "max_tokens": p.cfg.MaxTokens}
	if mt, ok := req["max_tokens"].(float64); ok && int(mt) < p.cfg.MaxTokens && mt > 0 {
		out["max_tokens"] = int(mt)
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	for k, v := range p.cfg.ExtraBody {
		out[k] = v
	}
	payload, _ := json.Marshal(out)
	up, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(p.cfg.Upstream, "/")+"/chat/completions", bytes.NewReader(payload))
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	up.Header.Set("HTTP-Referer", "https://agentsdk.acyclic.sh")
	up.Header.Set("X-Title", "Acyclic SDK sandbox")
	res, err := p.client.Do(up)
	if err != nil {
		fail(w, 502, "model upstream unreachable")
		return
	}
	defer res.Body.Close()
	resp, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 400 {
		fail(w, 502, "model upstream error "+res.Status)
		return
	}
	// Report the model the sandbox actually used.
	var j map[string]any
	if json.Unmarshal(resp, &j) == nil {
		j["model"] = p.cfg.Model
		resp, _ = json.Marshal(j)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}
