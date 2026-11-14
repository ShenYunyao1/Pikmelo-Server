package online

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const brokerResponseLimit = 4 << 20
const brokerBodyLimit = 2 << 20
const brokerReservation = brokerResponseLimit + brokerBodyLimit

type brokerGrant struct {
	context   context.Context
	expires   time.Time
	requests  int
	remaining int64
}

// Broker mediates the worker's temporary, bounded access to public HTTP APIs.
type Broker struct {
	client       *http.Client
	workerClient *http.Client
	baseURL      string
	mu           sync.Mutex
	grants       map[string]*brokerGrant
	executions   chan struct{}
	requests     chan struct{}
}

func NewBroker(client *http.Client, baseURL string) *Broker {
	if client == nil {
		client = NewHTTPClient()
	}
	if baseURL == "" {
		baseURL = "http://pikmelo:8080/api/v2/online/broker"
	}
	return &Broker{
		client: client, baseURL: baseURL, grants: make(map[string]*brokerGrant),
		executions: make(chan struct{}, 4), requests: make(chan struct{}, 4),
		workerClient: &http.Client{Timeout: 18 * time.Second, Transport: &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("worker redirect prohibited") }},
	}
}

func (b *Broker) Execute(ctx context.Context, workerURL, script, action string, request any) (json.RawMessage, error) {
	select {
	case b.executions <- struct{}{}:
		defer func() { <-b.executions }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	worker, err := url.Parse(workerURL)
	if err != nil || worker.User != nil || worker.Host == "" || (worker.Scheme != "http" && worker.Scheme != "https") {
		return nil, errors.New("online worker is not configured")
	}
	worker.Path = strings.TrimSuffix(worker.Path, "/") + "/execute"
	worker.RawQuery = ""
	worker.Fragment = ""
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return nil, errors.New("could not create worker capability")
	}
	token := hex.EncodeToString(entropy[:])
	b.mu.Lock()
	b.grants[token] = &brokerGrant{context: ctx, expires: time.Now().Add(20 * time.Second), requests: 32, remaining: 32 << 20}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.grants, token); b.mu.Unlock() }()
	payload, err := json.Marshal(map[string]any{"script": script, "action": action, "request": request, "broker": map[string]string{"url": b.baseURL, "token": token}})
	if err != nil || len(payload) > brokerBodyLimit {
		return nil, errors.New("source execution payload exceeds limit")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", worker.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid worker request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.workerClient.Do(req)
	if err != nil {
		return nil, errors.New("source worker is unavailable or timed out")
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, brokerResponseLimit)
	if err != nil || resp.StatusCode != 200 || !json.Valid(body) {
		return nil, errors.New("source initialization or request failed")
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(body, &result) != nil || result == nil || result["error"] != nil {
		return nil, errors.New("invalid source worker result")
	}
	if action == "init" && result["sources"] == nil || action == "request" && result["result"] == nil {
		return nil, errors.New("source worker returned an incomplete result")
	}
	return body, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(body)) > limit {
		return body, errors.New("response exceeds limit or could not be read")
	}
	return body, nil
}

func brokerReply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func brokerError(w http.ResponseWriter, status int, message string) {
	brokerReply(w, status, map[string]string{"error": message})
}

func (b *Broker) acquire(token string) (*brokerGrant, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	grant := b.grants[token]
	if grant == nil || grant.context.Err() != nil || !time.Now().Before(grant.expires) {
		return nil, false
	}
	return grant, true
}

func (b *Broker) reserve(grant *brokerGrant) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if grant.context.Err() != nil || !time.Now().Before(grant.expires) || grant.requests <= 0 || grant.remaining < brokerReservation {
		return false
	}
	grant.requests--
	grant.remaining -= brokerReservation
	return true
}

func validBrokerHeaders(headers map[string]string) bool {
	for key, value := range headers {
		name := strings.ToLower(key)
		switch name {
		case "host", "connection", "content-length", "transfer-encoding", "trailer", "te", "upgrade", "proxy-authorization", "proxy-authenticate", "proxy-connection", "forwarded":
			return false
		}
		if strings.HasPrefix(name, "x-forwarded-") || strings.ContainsAny(key+value, "\r\n") || strings.TrimSpace(key) != key || key == "" {
			return false
		}
	}
	return len(headers) <= 64
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		brokerError(w, 401, "invalid worker capability")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	grant, ok := b.acquire(token)
	if !ok {
		brokerError(w, 401, "invalid worker capability")
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		brokerError(w, 405, "POST required")
		return
	}
	select {
	case b.requests <- struct{}{}:
		defer func() { <-b.requests }()
	default:
		brokerError(w, 429, "worker HTTP capacity reached")
		return
	}
	if !b.reserve(grant) {
		brokerError(w, 429, "worker HTTP budget exhausted")
		return
	}
	var used int64
	defer func() { b.mu.Lock(); grant.remaining += brokerReservation - used; b.mu.Unlock() }()
	body, err := readBounded(r.Body, brokerBodyLimit)
	used = int64(len(body))
	if err != nil {
		brokerError(w, 413, "worker HTTP request exceeds limit")
		return
	}
	var payload struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if json.Unmarshal(body, &payload) != nil {
		brokerError(w, 400, "invalid worker HTTP request")
		return
	}
	if payload.Method == "" {
		payload.Method = "GET"
	}
	payload.Method = strings.ToUpper(payload.Method)
	upstream, err := url.Parse(payload.URL)
	if err != nil || validateURL(upstream) != nil || !validBrokerHeaders(payload.Headers) || payload.Method != "GET" && payload.Method != "POST" {
		brokerError(w, 400, "worker HTTP target, method or headers prohibited")
		return
	}
	var outgoing []byte
	if len(payload.Body) > 0 && string(payload.Body) != "null" {
		var text string
		if json.Unmarshal(payload.Body, &text) == nil {
			outgoing = []byte(text)
		} else {
			outgoing = payload.Body
		}
	}
	ctx, cancel := context.WithTimeout(grant.context, 14*time.Second)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	request, err := http.NewRequestWithContext(ctx, payload.Method, upstream.String(), bytes.NewReader(outgoing))
	if err != nil {
		brokerError(w, 400, "invalid worker HTTP request")
		return
	}
	for key, value := range payload.Headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Accept-Encoding", "gzip, deflate")
	response, err := b.client.Do(request)
	if err != nil {
		brokerError(w, 502, "public endpoint request failed")
		return
	}
	defer response.Body.Close()
	raw, err := readBounded(response.Body, brokerResponseLimit)
	used += min(int64(len(raw)), int64(brokerResponseLimit))
	if err != nil {
		brokerError(w, 502, "public endpoint response exceeds limit")
		return
	}
	decoded := raw
	var decompressor io.ReadCloser
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "gzip":
		decompressor, err = gzip.NewReader(bytes.NewReader(raw))
	case "deflate":
		decompressor, err = zlib.NewReader(bytes.NewReader(raw))
	case "", "identity":
	default:
		err = errors.New("unsupported response encoding")
	}
	if err == nil && decompressor != nil {
		decoded, err = readBounded(decompressor, brokerResponseLimit)
		decompressor.Close()
	}
	used += max(int64(0), min(int64(brokerResponseLimit), int64(len(decoded)))-int64(len(raw)))
	if err != nil {
		brokerError(w, 502, "public endpoint response decoding failed")
		return
	}
	headers := map[string]string{}
	for _, key := range []string{"Content-Type", "ETag", "Last-Modified", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(key); value != "" {
			headers[key] = value
		}
	}
	brokerReply(w, 200, map[string]any{"statusCode": response.StatusCode, "headers": headers, "body": string(decoded)})
}
