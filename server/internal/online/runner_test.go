package online

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type brokerTestTransport func(*http.Request) (*http.Response, error)

func (f brokerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBrokerExecuteCapabilityAndRevocation(t *testing.T) {
	client := &http.Client{Transport: brokerTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "example.com" {
			t.Error("unexpected upstream")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	broker := NewBroker(client, "")
	server := httptest.NewServer(broker)
	defer server.Close()
	broker.baseURL = server.URL
	var capability string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Broker struct{ URL, Token string } }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		capability = payload.Broker.Token
		request, _ := http.NewRequest("POST", payload.Broker.URL, strings.NewReader(`{"url":"https://example.com/song","method":"GET"}`))
		request.Header.Set("Authorization", "Bearer "+capability)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		var got struct {
			StatusCode int
			Body       string
		}
		if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if response.StatusCode != 200 || got.StatusCode != 200 || got.Body != `{"ok":true}` {
			t.Errorf("invalid broker response %+v", got)
		}
		w.Write([]byte(`{"sources":{"kw":{}}}`))
	}))
	defer worker.Close()
	result, err := broker.Execute(context.Background(), worker.URL, "fixture", "init", nil)
	if err != nil || !bytes.Contains(result, []byte("sources")) {
		t.Fatalf("execute %s %v", result, err)
	}
	request := httptest.NewRequest("POST", "http://broker/", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+capability)
	response := httptest.NewRecorder()
	broker.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("capability survived execution: %d", response.Code)
	}
}

func TestBrokerRequestBudgetAndCompressedResponseLimit(t *testing.T) {
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	_, _ = zip.Write(bytes.Repeat([]byte("x"), brokerResponseLimit+1))
	_ = zip.Close()
	client := &http.Client{Transport: brokerTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/bomb" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": {"gzip"}}, Body: io.NopCloser(bytes.NewReader(compressed.Bytes()))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	broker := NewBroker(client, "")
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Broker struct{ Token string } }
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for i := 0; i < 33; i++ {
			request := httptest.NewRequest("POST", "http://broker/", strings.NewReader(`{"url":"https://example.com/","method":"GET"}`))
			request.Header.Set("Authorization", "Bearer "+payload.Broker.Token)
			response := httptest.NewRecorder()
			broker.ServeHTTP(response, request)
			expected := 200
			if i == 32 {
				expected = 429
			}
			if response.Code != expected {
				t.Errorf("request %d returned %d", i, response.Code)
			}
		}
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	_, err := broker.Execute(context.Background(), worker.URL, "fixture", "request", nil)
	worker.Close()
	if err != nil {
		t.Fatal(err)
	}
	broker.mu.Lock()
	broker.grants["bomb"] = &brokerGrant{context: context.Background(), expires: time.Now().Add(time.Second), requests: 32, remaining: 32 << 20}
	broker.mu.Unlock()
	request := httptest.NewRequest("POST", "http://broker/", strings.NewReader(`{"url":"https://example.com/bomb","method":"GET"}`))
	request.Header.Set("Authorization", "Bearer bomb")
	response := httptest.NewRecorder()
	broker.ServeHTTP(response, request)
	if response.Code != 502 {
		t.Fatalf("compressed bomb returned %d", response.Code)
	}
	broker.mu.Lock()
	remaining := broker.grants["bomb"].remaining
	broker.mu.Unlock()
	if remaining > 28<<20 {
		t.Fatal("oversized decompression did not consume budget")
	}
}

func TestBrokerRevokesOnWorkerFailureAndCancellation(t *testing.T) {
	broker := NewBroker(nil, "")
	for _, status := range []int{500, 200} {
		worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"script failure"}`))
		}))
		_, err := broker.Execute(context.Background(), worker.URL, "fixture", "request", nil)
		worker.Close()
		if err == nil {
			t.Fatal("worker failure was accepted")
		}
		broker.mu.Lock()
		count := len(broker.grants)
		broker.mu.Unlock()
		if count != 0 {
			t.Fatal("failure retained capabilities")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := broker.Execute(ctx, "http://127.0.0.1:1", "fixture", "init", nil); err == nil {
		t.Fatal("cancelled request accepted")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.grants) != 0 {
		t.Fatal("cancellation retained capabilities")
	}
}

func TestBrokerRejectsInvalidCapabilitiesAndTargets(t *testing.T) {
	broker := NewBroker(nil, "")
	request := httptest.NewRequest("POST", "http://broker/", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	broker.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("missing capability %d", response.Code)
	}
	for _, raw := range []string{`{"url":"http://127.0.0.1"}`, `{"url":"https://example.com","headers":{"Host":"evil"}}`, `{"url":"https://example.com","method":"CONNECT"}`} {
		worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct{ Broker struct{ Token string } }
			json.NewDecoder(r.Body).Decode(&payload)
			request := httptest.NewRequest("POST", "http://broker/", strings.NewReader(raw))
			request.Header.Set("Authorization", "Bearer "+payload.Broker.Token)
			response := httptest.NewRecorder()
			broker.ServeHTTP(response, request)
			if response.Code != 400 {
				t.Errorf("invalid target accepted %d", response.Code)
			}
			w.Write([]byte(`{"result":"ok"}`))
		}))
		_, err := broker.Execute(context.Background(), worker.URL, "fixture", "request", nil)
		worker.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
