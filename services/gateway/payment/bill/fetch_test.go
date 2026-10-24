package bill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type fixedResolver []net.IPAddr

func (r fixedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr(r), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFetchBillURLPinsProviderHostAndBoundsContent(t *testing.T) {
	resolver := fixedResolver{{IP: net.ParseIP("1.1.1.1")}}
	var calls int
	body := []byte("raw bill bytes")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "files.example.com" || req.URL.RawQuery != "token=private" {
			t.Fatalf("unexpected requested URL: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body))}, nil
	})
	got, err := FetchBillURL(context.Background(), "https://files.example.com/bill?token=private", []string{"files.example.com"}, nil, resolver, transport)
	if err != nil || string(got) != string(body) || calls != 1 {
		t.Fatalf("download=%q calls=%d err=%v", got, calls, err)
	}

	for _, raw := range []string{
		"http://files.example.com/bill",
		"https://files.example.com.evil.test/bill",
		"https://user@files.example.com/bill",
		"https://files.example.com/bill#fragment",
	} {
		if _, err := FetchBillURL(context.Background(), raw, []string{"files.example.com"}, nil, resolver, transport); !errors.Is(err, ErrUnsafeDownloadURL) {
			t.Fatalf("unsafe URL %q accepted: %v", raw, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unsafe URL reached transport; calls=%d", calls)
	}
}

func TestFetchBillURLRejectsPrivateReservedAndCompressedResponses(t *testing.T) {
	for _, ipText := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "2001:db8::1"} {
		ip := netip.MustParseAddr(ipText)
		parsed := net.IP(ip.AsSlice())
		_, err := FetchBillURL(context.Background(), "https://files.example.com/bill", []string{"files.example.com"}, nil, fixedResolver{{IP: parsed}}, roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("private/reserved address reached transport")
			return nil, nil
		}))
		if !errors.Is(err, ErrUnsafeDownloadURL) {
			t.Fatalf("address %s should be rejected, got %v", ipText, err)
		}
	}
	compressed := roundTripFunc(func(*http.Request) (*http.Response, error) {
		headers := make(http.Header)
		headers.Set("Content-Encoding", "gzip")
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader("compressed")), ContentLength: 10}, nil
	})
	if _, err := FetchBillURL(context.Background(), "https://files.example.com/bill", []string{"files.example.com"}, nil, fixedResolver{{IP: net.ParseIP("1.1.1.1")}}, compressed); err == nil {
		t.Fatal("compressed response accepted")
	}
}

func TestFetchBillURLDoesNotLeakShortLivedURLAndRejectsRedirect(t *testing.T) {
	resolver := fixedResolver{{IP: net.ParseIP("1.1.1.1")}}
	secretURL := "https://files.example.com/bill?token=do-not-log-this"
	_, err := FetchBillURL(context.Background(), secretURL, []string{"files.example.com"}, nil, resolver, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("synthetic transport error at %s", req.URL)
	}))
	if err == nil || strings.Contains(err.Error(), "do-not-log-this") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("download error leaked capability URL: %v", err)
	}

	var calls int
	redirect := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://files.example.com/second?token=other"}}, Body: io.NopCloser(strings.NewReader("")), ContentLength: 0}, nil
	})
	if _, err := FetchBillURL(context.Background(), secretURL, []string{"files.example.com"}, nil, resolver, redirect); err == nil || calls != 1 {
		t.Fatalf("redirect followed or accepted: calls=%d err=%v", calls, err)
	}
}

func TestFetchBillURLEnforcesDeclaredAndStreamingSizeLimits(t *testing.T) {
	resolver := fixedResolver{{IP: net.ParseIP("1.1.1.1")}}
	url := "https://files.example.com/bill"
	tooLargeDeclared := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), ContentLength: MaxDownloadedBillBytes + 1}, nil
	})
	if _, err := FetchBillURL(context.Background(), url, []string{"files.example.com"}, nil, resolver, tooLargeDeclared); err == nil {
		t.Fatal("declared oversized response accepted")
	}

	for _, size := range []int64{MaxDownloadedBillBytes, MaxDownloadedBillBytes + 1} {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(&repeatReader{remaining: size}), ContentLength: -1}, nil
		})
		data, err := FetchBillURL(context.Background(), url, []string{"files.example.com"}, nil, resolver, transport)
		if size == MaxDownloadedBillBytes && (err != nil || int64(len(data)) != size) {
			t.Fatalf("exact limit rejected: bytes=%d err=%v", len(data), err)
		}
		if size > MaxDownloadedBillBytes && (err == nil || len(data) != 0) {
			t.Fatalf("stream beyond limit accepted: bytes=%d err=%v", len(data), err)
		}
	}
}

func TestFetchBillURLSurfacesOnlySafeCancellationAndReadErrors(t *testing.T) {
	resolver := fixedResolver{{IP: net.ParseIP("1.1.1.1")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})
	if _, err := FetchBillURL(ctx, "https://files.example.com/bill?token=secret", []string{"files.example.com"}, nil, resolver, transport); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("cancellation was not safely reported: %v", err)
	}
	readFailure := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(alwaysErrorReader{}), ContentLength: -1}, nil
	})
	if _, err := FetchBillURL(context.Background(), "https://files.example.com/bill", []string{"files.example.com"}, nil, resolver, readFailure); err == nil {
		t.Fatal("body read error accepted")
	}
}

type repeatReader struct{ remaining int64 }

func (r *repeatReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := int64(len(buffer))
	if count > r.remaining {
		count = r.remaining
	}
	for i := int64(0); i < count; i++ {
		buffer[i] = 'x'
	}
	r.remaining -= count
	return int(count), nil
}

type alwaysErrorReader struct{}

func (alwaysErrorReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
