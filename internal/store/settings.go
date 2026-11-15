package store

import (
	"encoding/json"
	"fmt"
)

// Settings are runtime-editable options stored in the settings table.
type Settings struct {
	ConsoleName          string `json:"console_name"`
	OpenRegistration     bool   `json:"open_registration"`     // devices may register with user credentials
	RegistrationApproval string `json:"registration_approval"` // auto | manual
	DefaultLevel         string `json:"default_level"`         // level for newly registered devices
	DiscoveryScope       string `json:"discovery_scope"`       // all | group | user
	MFARequired          bool   `json:"mfa_required"`          // panel users must enroll MFA
	RelayEnabled         bool   `json:"relay_enabled"`         // allow server relay for messages/files
	RelayMaxFileMB       int    `json:"relay_max_file_mb"`
	MessageRetentionDays int    `json:"message_retention_days"` // queued/delivered relay messages
	FileRetentionHours   int    `json:"file_retention_hours"`
	LogRetentionDays     int    `json:"log_retention_days"`
	BackupHour           int    `json:"backup_hour"` // -1 disables scheduled backups
	BackupKeep           int    `json:"backup_keep"`
	UpdateChannel        string `json:"update_channel"` // stable | beta
	AutoUpdate           bool   `json:"auto_update"`
	SessionHours         int    `json:"session_hours"` // panel login lifetime
	DeviceTokenHours     int    `json:"device_token_hours"`
	AbnormalFailLimit    int    `json:"abnormal_fail_limit"` // failed logins per IP / 10 min before flagging + temp block
	Theme                string `json:"theme"`               // light | dark | system (panel preference)
	Accent               string `json:"accent"`              // jade | terracotta | rose | olive | graphite
	Language             string `json:"language"`            // zh-TW | zh-CN | ja | en
}

func DefaultSettings() Settings {
	return Settings{
		ConsoleName:          "Meshport",
		OpenRegistration:     true,
		RegistrationApproval: "manual",
		DefaultLevel:         "guest",
		DiscoveryScope:       "all",
		MFARequired:          false,
		RelayEnabled:         true,
		RelayMaxFileMB:       2048,
		MessageRetentionDays: 30,
		FileRetentionHours:   72,
		LogRetentionDays:     14,
		BackupHour:           3,
		BackupKeep:           14,
		UpdateChannel:        "stable",
		AutoUpdate:           false,
		SessionHours:         12,
		DeviceTokenHours:     24,
		AbnormalFailLimit:    5,
		Theme:                "system",
		Accent:               "jade",
		Language:             "en",
	}
}

func (s Settings) Validate() error {
	in := func(v string, opts ...string) bool {
		for _, o := range opts {
			if v == o {
				return true
			}
		}
		return false
	}
	switch {
	case !in(s.RegistrationApproval, "auto", "manual"):
		return fmt.Errorf("registration_approval must be auto or manual")
	case !in(s.DefaultLevel, "admin", "member", "guest"):
		return fmt.Errorf("default_level must be admin, member or guest")
	case !in(s.DiscoveryScope, "all", "group", "user"):
		return fmt.Errorf("discovery_scope must be all, group or user")
	case !in(s.UpdateChannel, "stable", "beta"):
		return fmt.Errorf("update_channel must be stable or beta")
	case !in(s.Theme, "light", "dark", "system"):
		return fmt.Errorf("theme must be light, dark or system")
	case !in(s.Accent, "jade", "terracotta", "rose", "olive", "graphite"):
		return fmt.Errorf("unknown accent")
	case !in(s.Language, "zh-TW", "zh-CN", "ja", "en"):
		return fmt.Errorf("language must be zh-TW, zh-CN, ja or en")
	case s.RelayMaxFileMB < 1 || s.MessageRetentionDays < 1 || s.LogRetentionDays < 1 || s.FileRetentionHours < 1:
		return fmt.Errorf("retention and size limits must be positive")
	case s.BackupHour < -1 || s.BackupHour > 23 || s.BackupKeep < 1:
		return fmt.Errorf("backup_hour must be -1..23 and backup_keep >= 1")
	case s.SessionHours < 1 || s.DeviceTokenHours < 1 || s.AbnormalFailLimit < 1:
		return fmt.Errorf("session lifetimes and limits must be positive")
	}
	return nil
}

// Settings returns the current settings (cached).
func (db *DB) Settings() Settings {
	db.settingsMu.RLock()
	if db.settingsCache != nil {
		s := *db.settingsCache
		db.settingsMu.RUnlock()
		return s
	}
	db.settingsMu.RUnlock()
	s := DefaultSettings()
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='settings'`).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	db.settingsMu.Lock()
	db.settingsCache = &s
	db.settingsMu.Unlock()
	return s
}

func (db *DB) SaveSettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('settings', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(b)); err != nil {
		return err
	}
	db.settingsMu.Lock()
	db.settingsCache = &s
	db.settingsMu.Unlock()
	return nil
}

// KV helpers for small internal values (e.g. last update check).
func (db *DB) GetKV(key string) string {
	var v string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key=?`, "kv:"+key).Scan(&v)
	return v
}

func (db *DB) SetKV(key, value string) error {
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "kv:"+key, value)
	return err
}
