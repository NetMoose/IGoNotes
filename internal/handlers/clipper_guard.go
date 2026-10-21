package handlers

import (
	"context"
	"net/http"
	"strings"

	"IGoNotes/internal/model"
)

// ClipperAuthenticator validates an opaque token independently of HTTP parsing.
type ClipperAuthenticator interface {
	Authenticate(context.Context, string) error
}

// RequireClipper guards one /api/clipper/v1/ route with its explicit GET/POST
// methods. Later wiring must register the wrapped handler for OPTIONS as well
// as those methods (e.g. RequireClipper(auth, imports, "GET", "POST")), outside
// RequireLocalOrigin, with setup checks inside next. Never wrap ordinary or
// management APIs with this guard. No routes are registered here.
func RequireClipper(auth ClipperAuthenticator, next http.Handler, methods ...string) http.Handler {
	allowed := make(map[string]bool, len(methods))
	for _, method := range methods {
		if method == http.MethodGet || method == http.MethodPost {
			allowed[method] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Even denied/no-Origin responses vary, so caches cannot reuse them for
		// a different Origin. Add preserves Vary set by outer middleware.
		w.Header().Add("Vary", "Origin")
		defaultPort := "80"
		if r.TLS != nil {
			defaultPort = "443"
		}
		_, local := parseLocalEndpoint(r.Host, defaultPort)
		origins := r.Header.Values("Origin")
		origin := ""
		if len(origins) == 1 {
			origin = origins[0]
		}
		if !local || (len(origins) != 0 && (len(origins) != 1 || !validClipperOrigin(origin))) {
			WriteAPIError(w, http.StatusForbidden, "forbidden_origin", "Forbidden request", "")
			return
		}
		if r.Method == http.MethodOptions {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			requested := r.Header.Values("Access-Control-Request-Method")
			headers, validHeaders := clipperPreflightHeaders(r.Header.Values("Access-Control-Request-Headers"))
			if origin == "" || len(requested) != 1 || !allowed[requested[0]] || !validHeaders {
				WriteAPIError(w, http.StatusForbidden, "forbidden_origin", "Forbidden request", "")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", requested[0])
			if headers != "" {
				w.Header().Set("Access-Control-Allow-Headers", headers)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		values := r.Header.Values("Authorization")
		token := ""
		if len(values) == 1 && len(values[0]) > 7 && strings.EqualFold(values[0][:7], "Bearer ") {
			token = values[0][7:]
		}
		if !validClipperBearer(token) {
			writeServiceError(w, &model.ClipperError{Code: model.ClipperUnauthorized})
			return
		}
		if err := auth.Authenticate(r.Context(), token); err != nil {
			writeServiceError(w, err)
			return
		}
		if !allowed[r.Method] {
			var supported []string
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				if allowed[method] {
					supported = append(supported, method)
				}
			}
			supported = append(supported, http.MethodOptions)
			w.Header().Set("Allow", strings.Join(supported, ", "))
			WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
			return
		}
		// Fetch metadata is not authorization: extension fetch may be cross-site,
		// and native clients may omit it. Both still require the Bearer above.
		next.ServeHTTP(w, r)
	})
}

func validClipperOrigin(origin string) bool {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(origin, prefix) || len(origin) != len(prefix)+32 {
		return false
	}
	for _, ch := range origin[len(prefix):] {
		if ch < 'a' || ch > 'p' {
			return false
		}
	}
	return true
}

// clipperPreflightHeaders validates the entire requested subset before returning
// canonical, deduplicated names in a stable order. No request means no header.
func clipperPreflightHeaders(values []string) (string, bool) {
	var authorization, contentType bool
	for _, value := range values {
		for _, header := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(header)) {
			case "authorization":
				authorization = true
			case "content-type":
				contentType = true
			default:
				return "", false
			}
		}
	}
	var headers []string
	if authorization {
		headers = append(headers, "Authorization")
	}
	if contentType {
		headers = append(headers, "Content-Type")
	}
	return strings.Join(headers, ", "), true
}

func validClipperBearer(token string) bool {
	if token == "" {
		return false
	}
	// RFC 6750 b64token: one or more token characters, then optional padding.
	data := strings.TrimRight(token, "=")
	if data == "" {
		return false
	}
	for _, ch := range data {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~+/", ch)) {
			return false
		}
	}
	return true
}
