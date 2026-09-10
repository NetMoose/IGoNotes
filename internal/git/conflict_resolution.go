package git

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"IGoNotes/internal/model"
)

type resolutionWrite struct {
	Path  string
	Stage int
	OID   string
	Mode  string
	Data  []byte
}

type resolutionPlan struct {
	RemovePaths []string
	Writes      []resolutionWrite
	StagePaths  []string
}

func cleanConflictPath(value, field string) (string, error) {
	if value == "" {
		return "", newConflictFieldError("Conflict path is required", field)
	}
	if strings.IndexByte(value, 0) >= 0 || path.IsAbs(value) || filepath.IsAbs(filepath.FromSlash(value)) || path.Clean(value) != value || !filepath.IsLocal(filepath.FromSlash(value)) {
		return "", newConflictFieldError("Invalid conflict path", field)
	}
	for _, component := range strings.Split(filepath.FromSlash(value), string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") {
			return "", newConflictFieldError("Invalid conflict path", field)
		}
	}
	return value, nil
}

func validateResolution(conflict Conflict, request model.GitConflictResolveRequest) error {
	if request.ConflictID != conflict.ID || request.Path != conflict.Path {
		return ErrConflictStale
	}
	if !conflictAllows(conflict, request.Action) {
		return newConflictFieldError("Invalid conflict action", "action")
	}

	switch request.Action {
	case model.GitConflictUseLocal:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if conflict.Local == nil {
			return newConflictFieldError("Local conflict stage is unavailable", "action")
		}
		if request.LocalOID != conflict.Local.OID || request.LocalOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "content", "local_path", "remote_path", "remote_oid")
	case model.GitConflictUseRemote:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if conflict.Remote == nil {
			return newConflictFieldError("Remote conflict stage is unavailable", "action")
		}
		if request.RemoteOID != conflict.Remote.OID || request.RemoteOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "content", "local_path", "remote_path", "local_oid")
	case model.GitConflictManual:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if request.Content == nil {
			return newConflictFieldError("Manual conflict content is required", "content")
		}
		return rejectResolutionFields(request, "local_path", "remote_path", "local_oid", "remote_oid")
	case model.GitConflictKeepBoth:
		localPath, err := cleanConflictPath(request.LocalPath, "local_path")
		if err != nil {
			return err
		}
		remotePath, err := cleanConflictPath(request.RemotePath, "remote_path")
		if err != nil {
			return err
		}
		if localPath == remotePath {
			return newConflictFieldError("Conflict output paths must differ", "remote_path")
		}
		if conflict.Local == nil || conflict.Remote == nil {
			return newConflictFieldError("Conflict stage is unavailable", "action")
		}
		if request.LocalOID != conflict.Local.OID || request.LocalOID == "" || request.RemoteOID != conflict.Remote.OID || request.RemoteOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "result_path", "content")
	case model.GitConflictDelete:
		return rejectResolutionFields(request, "result_path", "content", "local_path", "remote_path", "local_oid", "remote_oid")
	default:
		return newConflictFieldError("Invalid conflict action", "action")
	}
}

func conflictAllows(conflict Conflict, action model.GitConflictAction) bool {
	for _, allowed := range conflict.Actions {
		if allowed == string(action) {
			return true
		}
	}
	return false
}

func rejectResolutionFields(request model.GitConflictResolveRequest, fields ...string) error {
	for _, field := range fields {
		var present bool
		switch field {
		case "result_path":
			present = request.ResultPath != ""
		case "content":
			present = request.Content != nil
		case "local_path":
			present = request.LocalPath != ""
		case "remote_path":
			present = request.RemotePath != ""
		case "local_oid":
			present = request.LocalOID != ""
		case "remote_oid":
			present = request.RemoteOID != ""
		}
		if present {
			return newConflictFieldError("Conflict field does not apply to selected action", field)
		}
	}
	return nil
}

func buildResolutionPlan(conflict Conflict, conflicts []Conflict, request model.GitConflictResolveRequest) (resolutionPlan, error) {
	if err := validateResolution(conflict, request); err != nil {
		return resolutionPlan{}, err
	}
	sources, err := resolutionSources(conflict)
	if err != nil {
		return resolutionPlan{}, err
	}

	plan := resolutionPlan{RemovePaths: []string{}, Writes: []resolutionWrite{}}
	switch request.Action {
	case model.GitConflictUseLocal:
		write, err := resolutionStageWrite(request.ResultPath, 2, conflict.Local)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, write)
	case model.GitConflictUseRemote:
		write, err := resolutionStageWrite(request.ResultPath, 3, conflict.Remote)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, write)
	case model.GitConflictManual:
		mode, err := resolutionManualMode(conflict)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, resolutionWrite{Path: request.ResultPath, Mode: mode, Data: []byte(*request.Content)})
	case model.GitConflictKeepBoth:
		local, err := resolutionStageWrite(request.LocalPath, 2, conflict.Local)
		if err != nil {
			return resolutionPlan{}, err
		}
		remote, err := resolutionStageWrite(request.RemotePath, 3, conflict.Remote)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, local, remote)
	case model.GitConflictDelete:
	default:
		return resolutionPlan{}, newConflictFieldError("Invalid conflict action", "action")
	}

	outputs := make(map[string]struct{}, len(plan.Writes))
	for _, write := range plan.Writes {
		outputs[write.Path] = struct{}{}
	}
	for _, source := range sources {
		if _, reused := outputs[source]; !reused {
			plan.RemovePaths = append(plan.RemovePaths, source)
		}
	}
	if err := rejectResolutionCollision(conflict, conflicts, append(append([]string(nil), sources...), resolutionWritePaths(plan.Writes)...)); err != nil {
		return resolutionPlan{}, err
	}
	return sortResolutionPlan(plan), nil
}

func resolutionSources(conflict Conflict) ([]string, error) {
	primary, err := cleanConflictPath(conflict.Path, "path")
	if err != nil {
		return nil, err
	}
	sources := []string{primary}
	if conflict.OriginalPath == "" {
		return sources, nil
	}
	original, err := cleanConflictPath(conflict.OriginalPath, "original_path")
	if err != nil {
		return nil, err
	}
	if original != primary {
		sources = append(sources, original)
	}
	return sources, nil
}

func resolutionStageWrite(output string, stage int, source *ConflictStage) (resolutionWrite, error) {
	if source == nil || source.OID == "" || !validResolutionMode(source.Mode) {
		return resolutionWrite{}, newConflictFieldError("Conflict stage is unsupported", "action")
	}
	return resolutionWrite{Path: output, Stage: stage, OID: source.OID, Mode: source.Mode}, nil
}

func resolutionManualMode(conflict Conflict) (string, error) {
	if conflict.Local != nil {
		if !validResolutionMode(conflict.Local.Mode) {
			return "", newConflictFieldError("Conflict stage is unsupported", "action")
		}
		return conflict.Local.Mode, nil
	}
	if conflict.Remote != nil {
		if !validResolutionMode(conflict.Remote.Mode) {
			return "", newConflictFieldError("Conflict stage is unsupported", "action")
		}
		return conflict.Remote.Mode, nil
	}
	return "100644", nil
}

func validResolutionMode(mode string) bool {
	return mode == "100644" || mode == "100755" || mode == "120000"
}

func rejectResolutionCollision(selected Conflict, conflicts []Conflict, paths []string) error {
	requested := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		requested[value] = struct{}{}
	}
	for _, other := range conflicts {
		if other.ID == selected.ID && other.Path == selected.Path && other.OriginalPath == selected.OriginalPath {
			continue
		}
		for _, value := range []string{other.Path, other.OriginalPath} {
			if value == "" {
				continue
			}
			if _, overlaps := requested[value]; overlaps {
				return newConflictFieldError("Conflict paths overlap another unresolved conflict", "path")
			}
		}
	}
	return nil
}

func resolutionWritePaths(writes []resolutionWrite) []string {
	paths := make([]string, 0, len(writes))
	for _, write := range writes {
		paths = append(paths, write.Path)
	}
	return paths
}

func sortResolutionPlan(plan resolutionPlan) resolutionPlan {
	sort.Strings(plan.RemovePaths)
	sort.Slice(plan.Writes, func(i, j int) bool {
		if plan.Writes[i].Path != plan.Writes[j].Path {
			return plan.Writes[i].Path < plan.Writes[j].Path
		}
		if plan.Writes[i].Stage != plan.Writes[j].Stage {
			return plan.Writes[i].Stage < plan.Writes[j].Stage
		}
		if plan.Writes[i].OID != plan.Writes[j].OID {
			return plan.Writes[i].OID < plan.Writes[j].OID
		}
		return plan.Writes[i].Mode < plan.Writes[j].Mode
	})
	stagePaths := make(map[string]struct{}, len(plan.RemovePaths)+len(plan.Writes))
	for _, remove := range plan.RemovePaths {
		stagePaths[remove] = struct{}{}
	}
	for _, write := range plan.Writes {
		stagePaths[write.Path] = struct{}{}
	}
	plan.StagePaths = make([]string, 0, len(stagePaths))
	for stagePath := range stagePaths {
		plan.StagePaths = append(plan.StagePaths, stagePath)
	}
	sort.Strings(plan.StagePaths)
	return plan
}
