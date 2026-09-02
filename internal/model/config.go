package model

import "strings"

// Config представляет конфигурацию приложения
type Config struct {
	BaseDir        string `json:"base_dir"`
	Bases          []Base `json:"bases"`
	CurrentBase    string `json:"current_base"`
	SetupCompleted *bool  `json:"setup_completed"`
}

// Base представляет базу заметок
type Base struct {
	Name                     string `json:"name"`
	Path                     string `json:"path"`
	GitURL                   string `json:"git_url,omitempty"`
	GitBranch                string `json:"git_branch,omitempty"`
	AutoSync                 bool   `json:"auto_sync"`
	AutoSyncIntervalMinutes  int    `json:"auto_sync_interval_minutes,omitempty"`
	GitCommitMessageTemplate string `json:"git_commit_message_template,omitempty"`
}

func (b Base) GitConfigured() bool {
	return strings.TrimSpace(b.GitURL) != "" && strings.TrimSpace(b.GitBranch) != ""
}
