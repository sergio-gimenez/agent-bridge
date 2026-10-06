package ocs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type SetupConfig struct {
	Version      int                         `json:"version"`
	Profiles     map[string]SetupProfile     `json:"profiles"`
	Destinations map[string]SetupDestination `json:"destinations,omitempty"`
}

type SetupDestination struct {
	MCPConfig string `json:"mcpConfig,omitempty"`
	SkillsDir string `json:"skillsDir,omitempty"`
}

type SetupProfile struct {
	Targets   []string                 `json:"targets"`
	Skills    map[string]SetupSkill    `json:"skills,omitempty"`
	MCPs      map[string]SetupMCP      `json:"mcps,omitempty"`
	Overrides map[string]SetupOverride `json:"overrides,omitempty"`
}

type SetupOverride struct {
	Skills map[string]SetupSkill `json:"skills,omitempty"`
	MCPs   map[string]SetupMCP   `json:"mcps,omitempty"`
}

type SetupSkill struct {
	Path    string `json:"path,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type SetupMCP struct {
	Transport      string   `json:"transport,omitempty"`
	URL            string   `json:"url,omitempty"`
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	EnvVars        []string `json:"envVars,omitempty"`
	BearerTokenEnv string   `json:"bearerTokenEnv,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

var setupName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
var envName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func parseSetup(value any) (*SetupConfig, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var setup SetupConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&setup); err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}
	if setup.Version != 1 {
		return nil, fmt.Errorf("setup.version must be 1")
	}
	return &setup, nil
}

func setupEnabled(value *bool) bool { return value == nil || *value }

func setupPath(value, base string) (string, error) {
	value = expandHome(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Abs(value)
}

func mergedSkills(profile SetupProfile, target string) map[string]SetupSkill {
	merged := make(map[string]SetupSkill)
	for name, skill := range profile.Skills {
		merged[name] = skill
	}
	for name, override := range profile.Overrides[target].Skills {
		skill := merged[name]
		if override.Path != "" {
			skill.Path = override.Path
		}
		if override.Enabled != nil {
			skill.Enabled = override.Enabled
		}
		merged[name] = skill
	}
	return merged
}

func mergedMCPs(profile SetupProfile, target string) map[string]SetupMCP {
	merged := make(map[string]SetupMCP)
	for name, mcp := range profile.MCPs {
		merged[name] = mcp
	}
	for name, override := range profile.Overrides[target].MCPs {
		mcp := merged[name]
		if override.Transport != "" {
			mcp.Transport = override.Transport
		}
		if override.URL != "" {
			mcp.URL = override.URL
		}
		if override.Command != "" {
			mcp.Command = override.Command
		}
		if override.Args != nil {
			mcp.Args = override.Args
		}
		if override.EnvVars != nil {
			mcp.EnvVars = override.EnvVars
		}
		if override.BearerTokenEnv != "" {
			mcp.BearerTokenEnv = override.BearerTokenEnv
		}
		if override.Enabled != nil {
			mcp.Enabled = override.Enabled
		}
		merged[name] = mcp
	}
	return merged
}

func (mcp SetupMCP) native(tool Source) (map[string]any, error) {
	entry := make(map[string]any)
	if mcp.Transport == "http" {
		endpoint, err := url.Parse(mcp.URL)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil {
			return nil, fmt.Errorf("HTTP MCP requires an http(s) URL without embedded credentials")
		}
		if mcp.Command != "" || len(mcp.Args) > 0 || len(mcp.EnvVars) > 0 {
			return nil, fmt.Errorf("HTTP MCP cannot specify command, args, or envVars")
		}
		entry["url"] = mcp.URL
		if tool == SourceClaude {
			entry["type"] = "http"
		}
		if tool == SourceOpencode {
			entry["type"], entry["enabled"] = "remote", true
		}
		if mcp.BearerTokenEnv != "" {
			if !envName.MatchString(mcp.BearerTokenEnv) {
				return nil, fmt.Errorf("invalid bearerTokenEnv name")
			}
			switch tool {
			case SourceCodex:
				entry["bearer_token_env_var"] = mcp.BearerTokenEnv
			case SourceClaude:
				entry["headers"] = map[string]string{"Authorization": "Bearer ${" + mcp.BearerTokenEnv + "}"}
			case SourceOpencode:
				entry["headers"] = map[string]string{"Authorization": "Bearer {env:" + mcp.BearerTokenEnv + "}"}
				entry["oauth"] = false
			}
		}
	} else if mcp.Transport == "stdio" {
		if strings.TrimSpace(mcp.Command) == "" || mcp.URL != "" || mcp.BearerTokenEnv != "" {
			return nil, fmt.Errorf("stdio MCP requires a command and cannot specify URL or bearerTokenEnv")
		}
		args := mcp.Args
		if args == nil {
			args = []string{}
		}
		entry["command"], entry["args"] = mcp.Command, args
		if tool == SourceOpencode {
			entry["type"], entry["enabled"], entry["command"] = "local", true, append([]string{mcp.Command}, args...)
			delete(entry, "args")
		}
		if len(mcp.EnvVars) > 0 {
			env := make(map[string]string)
			for _, name := range mcp.EnvVars {
				if !envName.MatchString(name) {
					return nil, fmt.Errorf("invalid envVars name")
				}
				value := "${" + name + "}"
				if tool == SourceOpencode {
					value = "{env:" + name + "}"
				}
				env[name] = value
			}
			if tool == SourceCodex {
				entry["env_vars"] = mcp.EnvVars
			} else if tool == SourceOpencode {
				entry["environment"] = env
			} else {
				entry["env"] = env
			}
		}
	} else {
		return nil, fmt.Errorf("transport must be http or stdio")
	}
	return entry, nil
}
