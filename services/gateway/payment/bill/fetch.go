package bill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

const MaxDownloadedBillBytes int64 = 32 << 20

var ErrUnsafeDownloadURL = errors.New("provider returned an unsafe bill download URL")

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// FetchBillURL accepts only a provider-owned HTTPS hostname and dials the
// already-validated public addresses, avoiding redirects, proxy routing, and
// DNS rebinding into local/private networks. transport is an explicit test
// seam; production callers pass nil and receive the pinned safe transport.
func FetchBillURL(ctx context.Context, rawURL string, allowedHosts []string, headers http.Header, resolver Resolver, transport http.RoundTripper) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, ErrUnsafeDownloadURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	fullHost := strings.ToLower(u.Host)
	allowed := false
	for _, candidate := range allowedHosts {
		candidate = strings.ToLower(candidate)
		if (candidate == host && (port == "" || port == "443")) || candidate == fullHost {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, ErrUnsafeDownloadURL
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrUnsafeDownloadURL
	}
	publicIPs := make([]netip.Addr, 0, len(addresses))
	for _, result := range addresses {
		ip, ok := netip.AddrFromSlice(result.IP)
		if !ok {
			return nil, ErrUnsafeDownloadURL
		}
		ip = ip.Unmap()
		if !isPublicIP(ip) {
			return nil, ErrUnsafeDownloadURL
		}
		publicIPs = append(publicIPs, ip)
	}
	sort.Slice(publicIPs, func(i, j int) bool { return publicIPs[i].Less(publicIPs[j]) })

	var client *http.Client
	if transport == nil {
		if port == "" {
			port = "443"
		}
		dialer := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
		pinned := &http.Transport{
			Proxy:                 nil,
			DisableCompression:    true,
			TLSHandshakeTimeout:   4 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			IdleConnTimeout:       30 * time.Second,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				requestHost, requestPort, splitErr := net.SplitHostPort(address)
				expectedPort := port
				if splitErr != nil || strings.ToLower(requestHost) != host || requestPort != expectedPort {
					return nil, ErrUnsafeDownloadURL
				}
				var lastErr error
				for _, ip := range publicIPs {
					conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), expectedPort))
					if dialErr == nil {
						return conn, nil
					}
					lastErr = dialErr
				}
				return nil, lastErr
			},
		}
		client = &http.Client{Transport: pinned, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		client = &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrUnsafeDownloadURL
	}
	req.Header.Set("Accept", "application/octet-stream")
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("bill download request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bill download returned HTTP %d", resp.StatusCode)
	}
	if encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, errors.New("compressed bill downloads are not supported")
	}
	if resp.ContentLength > MaxDownloadedBillBytes {
		return nil, errors.New("bill download exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxDownloadedBillBytes+1))
	if err != nil {
		return nil, errors.New("bill download could not be read")
	}
	if int64(len(data)) > MaxDownloadedBillBytes {
		return nil, errors.New("bill download exceeds size limit")
	}
	return data, nil
}

func isPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	blocked := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001::/23", "2001:db8::/32", "3fff::/20",
	}
	for _, raw := range blocked {
		prefix := netip.MustParsePrefix(raw)
		if prefix.Contains(ip) {
			return false
		}
	}
	if ip.Is6() {
		return netip.MustParsePrefix("2000::/3").Contains(ip)
	}
	return true
}

func SHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
