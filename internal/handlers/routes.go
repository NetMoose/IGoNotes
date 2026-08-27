package handlers

import (
	"net/http"
	"sort"
	"strings"
)

func methods(handlers map[string]http.Handler) http.Handler {
	allowed := make([]string, 0, len(handlers))
	for method := range handlers {
		allowed = append(allowed, method)
	}
	sort.Strings(allowed)
	allow := strings.Join(allowed, ", ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, ok := handlers[r.Method]
		if !ok {
			w.Header().Set("Allow", allow)
			WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "")
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func NewRouter(note *NoteHandler, settings *SettingsHandler, state SetupState, spa http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	api := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, RequireLocalOrigin(handler))
	}
	api("/api/info", methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(note.GetInfo),
	}))
	api("/api/notes", methods(map[string]http.Handler{
		http.MethodGet:  RequireSetup(state, http.HandlerFunc(note.GetNotes)),
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.CreateNote)),
	}))
	api("/api/note", methods(map[string]http.Handler{
		http.MethodGet:    RequireSetup(state, http.HandlerFunc(note.GetNote)),
		http.MethodDelete: RequireSetup(state, http.HandlerFunc(note.DeleteNote)),
	}))
	api("/api/sync", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.SyncNotes)),
	}))
	api("/api/raw", methods(map[string]http.Handler{
		http.MethodGet: RequireSetup(state, http.HandlerFunc(note.GetRawFile)),
	}))
	api("/api/save", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.SaveNote)),
	}))
	api("/api/rename", methods(map[string]http.Handler{
		http.MethodPut: RequireSetup(state, http.HandlerFunc(note.RenameNote)),
	}))
	api("/api/assets", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.UploadAsset)),
	}))

	api("/api/config", methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(settings.GetConfig),
		http.MethodPut: http.HandlerFunc(settings.SaveConfig),
	}))
	api("/api/setup", methods(map[string]http.Handler{
		http.MethodPost: http.HandlerFunc(settings.CompleteSetup),
	}))
	api("/api/bases", methods(map[string]http.Handler{
		http.MethodPost:   http.HandlerFunc(settings.AddBase),
		http.MethodPut:    http.HandlerFunc(settings.UpdateBase),
		http.MethodDelete: http.HandlerFunc(settings.ForgetBase),
	}))
	api("/api/bases/switch", methods(map[string]http.Handler{
		http.MethodPost: http.HandlerFunc(settings.SwitchBase),
	}))

	mux.Handle("/", spa)
	return mux
}
