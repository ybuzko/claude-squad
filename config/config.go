package config

import (
	"claude-squad/log"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ConfigFileName = "config.json"
	defaultProgram = "claude"
)

// GetConfigDir returns the path to the application's configuration directory
func GetConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config home directory: %w", err)
	}
	return filepath.Join(homeDir, ".claude-squad"), nil
}

// Profile represents a named program configuration
type Profile struct {
	Name    string `json:"name"`
	Program string `json:"program"`
}

// LinearConfig drives the read-only Linear poller that auto-spawns ticket sessions.
type LinearConfig struct {
	Enabled bool `json:"enabled"`
	// APIKey is a Linear personal API key. The LINEAR_API_KEY environment variable
	// takes precedence when set.
	APIKey string `json:"api_key,omitempty"`
	// ViewID is the UUID of the Linear custom view that is the source of tickets.
	ViewID          string `json:"view_id"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	// MaxConcurrent caps instances that are actively working (running or loading).
	// Blocked, idle, paused and done instances do not count.
	MaxConcurrent int `json:"max_concurrent"`
	// DoneStateTypes are Linear workflow state types (completed, canceled, ...) that
	// mark a ticket's instance as Done.
	DoneStateTypes []string `json:"done_state_types"`
	// DoneStateIDs are specific workflow state UUIDs that also count as Done.
	DoneStateIDs []string `json:"done_state_ids,omitempty"`
}

// SpawnConfig describes how a ticket session is launched.
type SpawnConfig struct {
	// Program is the command run in the tmux session; {ISSUE_ID} is substituted.
	Program string `json:"program"`
	// RepoPath is the main checkout that worktrees are created from.
	RepoPath string `json:"repo_path"`
	// BranchPrefix is prepended to ticket branch names, e.g. "agent/".
	BranchPrefix string `json:"branch_prefix"`
	// SetupCommand is run by the shell inside every new worktree before the program
	// starts (and again when a paused session's worktree is recreated), e.g.
	// "pnpm install --frozen-lockfile". Empty disables it.
	SetupCommand string `json:"setup_command,omitempty"`
	// ResumePrompt is sent to Claude when an archived ticket session is brought back
	// because its ticket returned to the view (or was requested with `t`). {ISSUE_ID}
	// is substituted.
	ResumePrompt string `json:"resume_prompt,omitempty"`
}

// Config represents the application configuration
type Config struct {
	// DefaultProgram is the default program to run in new instances
	DefaultProgram string `json:"default_program"`
	// AutoYes is a flag to automatically accept all prompts.
	AutoYes bool `json:"auto_yes"`
	// DaemonPollInterval is the interval (ms) at which the daemon polls sessions for autoyes mode.
	DaemonPollInterval int `json:"daemon_poll_interval"`
	// BranchPrefix is the prefix used for git branches created by the application.
	BranchPrefix string `json:"branch_prefix"`
	// Profiles is a list of named program profiles.
	Profiles []Profile `json:"profiles,omitempty"`
	// InstanceLimit caps the total number of instances.
	InstanceLimit int          `json:"instance_limit"`
	Linear        LinearConfig `json:"linear"`
	Spawn         SpawnConfig  `json:"spawn"`
}

const (
	DefaultInstanceLimit         = 10
	defaultLinearPollIntervalSec = 60
	defaultLinearMaxConcurrent   = 5
	defaultSpawnProgram          = `claude "/triage-linear-ticket {ISSUE_ID}"`
	defaultSpawnBranchPrefix     = "agent/"
	defaultSpawnResumePrompt     = "{ISSUE_ID} is back in your queue (reopened or requested again). " +
		"Re-read the ticket and any comments added since you last looked, re-check your earlier " +
		"findings against current origin/main and current data, then continue from where you left off."
)

// applyDefaults fills zero-valued fields so configs written by older versions keep
// working, and so a hand-edited config only needs the keys the user cares about.
func (c *Config) applyDefaults() {
	if c.InstanceLimit <= 0 {
		c.InstanceLimit = DefaultInstanceLimit
	}
	if c.Linear.PollIntervalSec <= 0 {
		c.Linear.PollIntervalSec = defaultLinearPollIntervalSec
	}
	if c.Linear.MaxConcurrent <= 0 {
		c.Linear.MaxConcurrent = defaultLinearMaxConcurrent
	}
	if c.Linear.DoneStateTypes == nil {
		c.Linear.DoneStateTypes = []string{"completed", "canceled"}
	}
	if c.Spawn.Program == "" {
		c.Spawn.Program = defaultSpawnProgram
	}
	if c.Spawn.BranchPrefix == "" {
		c.Spawn.BranchPrefix = defaultSpawnBranchPrefix
	}
	if c.Spawn.ResumePrompt == "" {
		c.Spawn.ResumePrompt = defaultSpawnResumePrompt
	}
}

// LinearAPIKey returns the API key, preferring the LINEAR_API_KEY environment variable.
func (c *Config) LinearAPIKey() string {
	if key := os.Getenv("LINEAR_API_KEY"); key != "" {
		return key
	}
	return c.Linear.APIKey
}

// GetProgram returns the program to run. If Profiles is non-empty and
// DefaultProgram matches a profile name, that profile's Program is returned.
// Otherwise DefaultProgram is returned as-is.
func (c *Config) GetProgram() string {
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			return p.Program
		}
	}
	return c.DefaultProgram
}

// GetProfiles returns a unified list of profiles. If Profiles is defined,
// those are returned with the default profile first. Otherwise, a single
// profile is synthesized from DefaultProgram.
func (c *Config) GetProfiles() []Profile {
	if len(c.Profiles) == 0 {
		return []Profile{{Name: c.DefaultProgram, Program: c.DefaultProgram}}
	}
	// Reorder so the default profile comes first.
	profiles := make([]Profile, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			profiles = append(profiles, p)
			break
		}
	}
	for _, p := range c.Profiles {
		if p.Name != c.DefaultProgram {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	program, err := GetClaudeCommand()
	if err != nil {
		log.ErrorLog.Printf("failed to get claude command: %v", err)
		program = defaultProgram
	}

	cfg := &Config{
		DefaultProgram:     program,
		AutoYes:            false,
		DaemonPollInterval: 1000,
		BranchPrefix: func() string {
			user, err := user.Current()
			if err != nil || user == nil || user.Username == "" {
				log.ErrorLog.Printf("failed to get current user: %v", err)
				return "session/"
			}
			return fmt.Sprintf("%s/", strings.ToLower(user.Username))
		}(),
	}
	cfg.applyDefaults()
	return cfg
}

// GetClaudeCommand attempts to find the "claude" command in the user's shell
// It checks in the following order:
// 1. Shell alias resolution: using "which" command
// 2. PATH lookup
//
// If both fail, it returns an error.
func GetClaudeCommand() (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // Default to bash if SHELL is not set
	}

	// Force the shell to load the user's profile and then run the command
	// For zsh, source .zshrc; for bash, source .bashrc
	var shellCmd string
	if strings.Contains(shell, "zsh") {
		shellCmd = "source ~/.zshrc &>/dev/null || true; which claude"
	} else if strings.Contains(shell, "bash") {
		shellCmd = "source ~/.bashrc &>/dev/null || true; which claude"
	} else {
		shellCmd = "which claude"
	}

	cmd := exec.Command(shell, "-c", shellCmd)
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		path := strings.TrimSpace(string(output))
		if path != "" {
			// Check if the output is an alias definition and extract the actual path
			// Handle formats like "claude: aliased to /path/to/claude" or other shell-specific formats
			aliasRegex := regexp.MustCompile(`(?:aliased to|->|=)\s*([^\s]+)`)
			matches := aliasRegex.FindStringSubmatch(path)
			if len(matches) > 1 {
				path = matches[1]
			}
			return path, nil
		}
	}

	// Otherwise, try to find in PATH directly
	claudePath, err := exec.LookPath("claude")
	if err == nil {
		return claudePath, nil
	}

	return "", fmt.Errorf("claude command not found in aliases or PATH")
}

func LoadConfig() *Config {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return DefaultConfig()
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create and save default config if file doesn't exist
			defaultCfg := DefaultConfig()
			if saveErr := saveConfig(defaultCfg); saveErr != nil {
				log.WarningLog.Printf("failed to save default config: %v", saveErr)
			}
			return defaultCfg
		}

		log.WarningLog.Printf("failed to get config file: %v", err)
		return DefaultConfig()
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.ErrorLog.Printf("failed to parse config file: %v", err)
		return DefaultConfig()
	}
	config.applyDefaults()

	return &config
}

// saveConfig saves the configuration to disk
func saveConfig(config *Config) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}

// SaveConfig exports the saveConfig function for use by other packages
func SaveConfig(config *Config) error {
	return saveConfig(config)
}
