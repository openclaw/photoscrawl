package archive

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
)

type lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)

type modelHTTPRecorderKey struct{}

type modelHTTPRecorder struct {
	mu        sync.Mutex
	requests  int
	responses int
	endpoints map[string]struct{}
}

type recordingRoundTripper struct {
	base http.RoundTripper
}

func (t recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder, _ := req.Context().Value(modelHTTPRecorderKey{}).(*modelHTTPRecorder)
	if recorder != nil {
		recorder.recordRequest()
	}
	resp, err := t.base.RoundTrip(req)
	if recorder != nil && resp != nil {
		recorder.recordResponse(responseEndpoint(resp))
	}
	return resp, err
}

func (r *modelHTTPRecorder) recordRequest() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
}

func (r *modelHTTPRecorder) recordResponse(endpoint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses++
	if r.endpoints == nil {
		r.endpoints = map[string]struct{}{}
	}
	if endpoint != "" {
		r.endpoints[endpoint] = struct{}{}
	}
}

func (r *modelHTTPRecorder) snapshot() (int, int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests, r.responses, sortedEndpointSet(r.endpoints)
}

func validateLoopbackEndpoint(ctx context.Context, value string, lookup lookupIPAddrFunc) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse local model endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("local model endpoint must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("local model endpoint must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("local model endpoint must not include credentials, a query, or a fragment")
	}
	if _, err := resolveLoopbackIPs(ctx, parsed.Hostname(), lookup); err != nil {
		return "", fmt.Errorf("local model endpoint %q: %w", parsed.Host, err)
	}
	return parsed.String(), nil
}

func resolveLoopbackIPs(ctx context.Context, host string, lookup lookupIPAddrFunc) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("must resolve only to loopback addresses")
		}
		return []net.IP{ip}, nil
	}
	addresses, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("host resolved to no addresses")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !address.IP.IsLoopback() {
			return nil, fmt.Errorf("must resolve only to loopback addresses")
		}
		ips = append(ips, address.IP)
	}
	return ips, nil
}

func loopbackDialContext(lookup lookupIPAddrFunc) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse local model address: %w", err)
		}
		ips, err := resolveLoopbackIPs(ctx, host, lookup)
		if err != nil {
			return nil, fmt.Errorf("local model address %q: %w", address, err)
		}
		var dialErr error
		for _, ip := range ips {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			dialErr = err
		}
		return nil, fmt.Errorf("dial local model: %w", dialErr)
	}
}

func responseEndpoint(resp *http.Response) string {
	if resp == nil || resp.Request == nil || resp.Request.URL == nil {
		return ""
	}
	return resp.Request.URL.String()
}

func sortedEndpointSet(endpoints map[string]struct{}) []string {
	values := make([]string, 0, len(endpoints))
	for endpoint := range endpoints {
		values = append(values, endpoint)
	}
	sort.Strings(values)
	return values
}
