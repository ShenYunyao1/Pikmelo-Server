package online

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var reservedNetworks = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3",
	"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
}

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, value := range reservedNetworks {
		if netip.MustParsePrefix(value).Contains(address) {
			return false
		}
	}
	return true
}

func validateURL(value *url.URL) error {
	if value == nil || (value.Scheme != "http" && value.Scheme != "https") || value.User != nil || value.Hostname() == "" {
		return errors.New("only public HTTP(S) URLs without embedded credentials are supported")
	}
	host := strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return errors.New("local network URLs are prohibited")
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return errors.New("non-public network address is prohibited")
	}
	if port := value.Port(); port != "" && port != "80" && port != "443" {
		return errors.New("only HTTP(S) ports are supported")
	}
	return nil
}

type publicTransport struct {
	resolver  func(context.Context, string) ([]net.IPAddr, error)
	transport *http.Transport
}

func (p *publicTransport) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolver(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("public endpoint DNS lookup failed")
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return nil, errors.New("endpoint resolves to a prohibited network")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, address := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return connection, nil
		}
	}
	return nil, errors.New("public endpoint connection failed")
}

func (p *publicTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateURL(request.URL); err != nil {
		return nil, err
	}
	return p.transport.RoundTrip(request)
}

// NewHTTPClient pins each connection to a validated DNS result. Proxies and
// environment credentials are deliberately outside the online-source protocol.
func NewHTTPClient() *http.Client {
	p := &publicTransport{resolver: net.DefaultResolver.LookupIPAddr}
	p.transport = &http.Transport{DialContext: p.dial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16, MaxConnsPerHost: 4, MaxResponseHeaderBytes: 64 << 10}
	return &http.Client{Transport: p, Timeout: 2 * time.Minute, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many endpoint redirects")
		}
		if len(via) > 0 && (request.URL.Host != via[0].URL.Host || request.URL.Scheme != via[0].URL.Scheme) {
			safe := http.Header{}
			for _, name := range []string{"Accept", "Accept-Encoding", "User-Agent", "Content-Type", "Range", "If-Range"} {
				if values := request.Header.Values(name); len(values) > 0 {
					safe[name] = values
				}
			}
			request.Header = safe
		}
		return validateURL(request.URL)
	}}
}
