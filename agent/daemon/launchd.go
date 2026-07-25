package daemon

import (
	"html"
	"sort"
)

// LaunchdConfig describes the macOS launchd service.
type LaunchdConfig struct {
	Label        string
	Program      string
	ConfigPath   string
	StatePath    string
	Live         bool
	ChatQuery    string
	PollInterval string
	StdoutPath   string
	StderrPath   string
	Environment  map[string]string
}

// LaunchdProgramArguments returns the exact argv used by launchd.
func LaunchdProgramArguments(cfg LaunchdConfig) []string {
	args := []string{cfg.Program}
	if cfg.ConfigPath != "" {
		args = append(args, "--config", cfg.ConfigPath)
	}
	if cfg.StatePath != "" {
		args = append(args, "--state", cfg.StatePath)
	}
	args = append(args, "daemon", "run")
	if cfg.Live {
		args = append(args, "--live")
	}
	if cfg.ChatQuery != "" {
		args = append(args, "--chat-query", cfg.ChatQuery)
	}
	if cfg.PollInterval != "" {
		args = append(args, "--poll-interval", cfg.PollInterval)
	}
	return args
}

// LaunchdPlist renders a launchd plist for the foreground daemon command.
func LaunchdPlist(cfg LaunchdConfig) string {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + html.EscapeString(cfg.Label) + `</string>
  <key>ProgramArguments</key>
  <array>
`
	for _, arg := range LaunchdProgramArguments(cfg) {
		plist += `    <string>` + html.EscapeString(arg) + `</string>
`
	}
	plist += `  </array>
`
	if len(cfg.Environment) > 0 {
		keys := make([]string, 0, len(cfg.Environment))
		for key := range cfg.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		plist += `  <key>EnvironmentVariables</key>
  <dict>
`
		for _, key := range keys {
			plist += `    <key>` + html.EscapeString(key) + `</key>
    <string>` + html.EscapeString(cfg.Environment[key]) + `</string>
`
		}
		plist += `  </dict>
`
	}
	plist += `
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
`
	if cfg.StdoutPath != "" {
		plist += `  <key>StandardOutPath</key>
  <string>` + html.EscapeString(cfg.StdoutPath) + `</string>
`
	}
	if cfg.StderrPath != "" {
		plist += `  <key>StandardErrorPath</key>
  <string>` + html.EscapeString(cfg.StderrPath) + `</string>
`
	}
	plist += `
</dict>
</plist>
`
	return plist
}
