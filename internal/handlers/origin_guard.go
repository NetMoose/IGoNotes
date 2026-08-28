package handlers

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type originEndpoint struct {
	host string
	port string
}

func RequireLocalOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		defaultPort := "80"
		if r.TLS != nil {
			scheme = "https"
			defaultPort = "443"
		}

		requestEndpoint, ok := parseLocalEndpoint(r.Host, defaultPort)
		if !ok || hasCrossSiteFetchMetadata(r) || !hasMatchingOrigin(r, scheme, defaultPort, requestEndpoint) {
			WriteAPIError(w, http.StatusForbidden, "forbidden_origin", "Forbidden request", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasCrossSiteFetchMetadata(r *http.Request) bool {
	for _, value := range r.Header.Values("Sec-Fetch-Site") {
		if strings.EqualFold(strings.TrimSpace(value), "cross-site") {
			return true
		}
	}
	return false
}

func hasMatchingOrigin(r *http.Request, scheme, defaultPort string, requestEndpoint originEndpoint) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}

	origin, err := url.Parse(origins[0])
	if err != nil || origin.Scheme == "" || origin.Opaque != "" || origin.Host == "" || origin.User != nil {
		return false
	}
	if origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.RawFragment != "" {
		return false
	}
	if strings.Contains(origins[0], "#") {
		return false
	}
	if !strings.EqualFold(origin.Scheme, scheme) {
		return false
	}

	originEndpoint, ok := parseLocalEndpoint(origin.Host, defaultPort)
	return ok && originEndpoint == requestEndpoint
}

func parseLocalEndpoint(authority, defaultPort string) (originEndpoint, bool) {
	if authority == "" {
		return originEndpoint{}, false
	}

	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		switch {
		case strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]"):
			host = authority[1 : len(authority)-1]
			port = defaultPort
		case strings.Contains(authority, ":"):
			return originEndpoint{}, false
		default:
			host = authority
			port = defaultPort
		}
	}

	if strings.HasPrefix(authority, "[") {
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil {
			return originEndpoint{}, false
		}
	}
	host, ok := normalizeLocalHostname(host)
	if !ok {
		return originEndpoint{}, false
	}
	port, ok = normalizePort(port)
	if !ok {
		return originEndpoint{}, false
	}
	return originEndpoint{host: host, port: port}, true
}

func normalizeLocalHostname(host string) (string, bool) {
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return "localhost", true
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", false
	}
	return ip.String(), true
}

func normalizePort(port string) (string, bool) {
	if port == "" {
		return "", false
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", false
	}
	return strconv.Itoa(number), true
}
