package handlers

import "net/http"

type SetupState interface {
	SetupCompleted() bool
}

func RequireSetup(state SetupState, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !state.SetupCompleted() {
			WriteAPIError(w, http.StatusPreconditionRequired, "setup_required", "setup required", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
