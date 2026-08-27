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
	mux.Handle("/api/info", methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(note.GetInfo),
	}))
	mux.Handle("/api/notes", methods(map[string]http.Handler{
		http.MethodGet:  RequireSetup(state, http.HandlerFunc(note.GetNotes)),
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.CreateNote)),
	}))
	mux.Handle("/api/note", methods(map[string]http.Handler{
		http.MethodGet:    RequireSetup(state, http.HandlerFunc(note.GetNote)),
		http.MethodDelete: RequireSetup(state, http.HandlerFunc(note.DeleteNote)),
	}))
	mux.Handle("/api/sync", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.SyncNotes)),
	}))
	mux.Handle("/api/raw", methods(map[string]http.Handler{
		http.MethodGet: RequireSetup(state, http.HandlerFunc(note.GetRawFile)),
	}))
	mux.Handle("/api/save", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.SaveNote)),
	}))
	mux.Handle("/api/rename", methods(map[string]http.Handler{
		http.MethodPut: RequireSetup(state, http.HandlerFunc(note.RenameNote)),
	}))
	mux.Handle("/api/assets", methods(map[string]http.Handler{
		http.MethodPost: RequireSetup(state, http.HandlerFunc(note.UploadAsset)),
	}))

	mux.Handle("/api/config", methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(settings.GetConfig),
		http.MethodPut: http.HandlerFunc(settings.SaveConfig),
	}))
	mux.Handle("/api/setup", methods(map[string]http.Handler{
		http.MethodPost: http.HandlerFunc(settings.CompleteSetup),
	}))
	mux.Handle("/api/bases", methods(map[string]http.Handler{
		http.MethodPost:   http.HandlerFunc(settings.AddBase),
		http.MethodPut:    http.HandlerFunc(settings.UpdateBase),
		http.MethodDelete: http.HandlerFunc(settings.ForgetBase),
	}))
	mux.Handle("/api/bases/switch", methods(map[string]http.Handler{
		http.MethodPost: http.HandlerFunc(settings.SwitchBase),
	}))

	mux.Handle("/", spa)
	return mux
}
