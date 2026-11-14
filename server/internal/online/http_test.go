package online

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPublicNetworkBoundary(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "::ffff:127.0.0.1", "fc00::1", "100.100.100.200", "0.0.0.0", "192.0.2.1", "224.0.0.1", "198.18.0.1", "2001:db8::1"} {
		if publicIP(net.ParseIP(address)) {
			t.Errorf("accepted internal or reserved address %s", address)
		}
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(address)) {
			t.Errorf("rejected public address %s", address)
		}
	}
	for _, address := range []string{"file:///tmp/music", "http://user:pass@example.com", "http://localhost/a", "http://127.0.0.1/a", "http://[::1]/a", "https://example.com:22/a"} {
		parsed, _ := url.Parse(address)
		if validateURL(parsed) == nil {
			t.Errorf("accepted prohibited URL %s", address)
		}
	}
}

type redirectFixture func(*http.Request) (*http.Response, error)

func (f redirectFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSafeClientNeverReintroducesRedirectCredentials(t *testing.T) {
	client := NewHTTPClient()
	client.Transport = redirectFixture(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "sub.example.com" && (r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "") {
			t.Fatal("credentials forwarded to redirected origin")
		}
		location := ""
		if r.URL.Host == "example.com" {
			location = "https://sub.example.com/a"
		} else if r.URL.Path == "/a" {
			location = "https://sub.example.com/b"
		}
		status := 200
		if location != "" {
			status = 302
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	r, _ := http.NewRequest("GET", "https://example.com/start", nil)
	r.Header.Set("Authorization", "Bearer private")
	r.Header.Set("Cookie", "secret=private")
	r.Header.Set("X-Api-Key", "private")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestSafeClientRejectsInternalRedirectAndMixedDNS(t *testing.T) {
	client := NewHTTPClient()
	request, _ := http.NewRequest("GET", "http://localhost/a", nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("localhost request accepted")
	}
	transport := &publicTransport{resolver: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	}}
	if _, err := transport.dial(context.Background(), "tcp", "mixed.example:443"); err == nil {
		t.Fatal("mixed DNS accepted")
	}
	redirect, _ := http.NewRequest("GET", "http://169.254.169.254/latest/meta-data", nil)
	if client.CheckRedirect(redirect, []*http.Request{request}) == nil {
		t.Fatal("internal redirect accepted")
	}
}
