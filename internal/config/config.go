// Package config reads where the archive should talk to and with what
// credentials.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Profile is one Confluence site and the credentials for it.
type Profile struct {
	Name     string `json:"name,omitempty"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Token    string `json:"token"`
	Protocol string `json:"protocol,omitempty"`
}

// BaseURL is the site's root, with no trailing slash.
func (self *Profile) BaseURL() string {
	protocol := self.Protocol
	if protocol == "" {
		protocol = "https"
	}
	return fmt.Sprintf("%s://%s", protocol, self.Domain)
}

// Config is every profile this tool knows.
type Config struct {
	ActiveProfile string              `json:"active_profile"`
	Profiles      map[string]*Profile `json:"profiles"`
}

// Load reads the configuration.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config: no configuration at %s, run cf auth login", path)
		}
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	configuration := &Config{}
	if err := json.Unmarshal(content, configuration); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	if configuration.Profiles == nil {
		configuration.Profiles = map[string]*Profile{}
	}
	return configuration, nil
}

// LoadOrEmpty is Load, with a configuration that does not exist yet treated as
// an empty one. It is what a command that is about to write one wants.
func LoadOrEmpty() (*Config, error) {
	configuration, err := Load()
	if err == nil {
		return configuration, nil
	}
	path, pathErr := Path()
	if pathErr != nil {
		return nil, pathErr
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return &Config{Profiles: map[string]*Profile{}}, nil
	}
	return nil, err
}

// Remove drops a profile, and the active mark with it if that is the one.
func (self *Config) Remove(name string) error {
	if _, isKnown := self.Profiles[name]; !isKnown {
		return fmt.Errorf("config: no profile named %q", name)
	}
	delete(self.Profiles, name)
	if self.ActiveProfile == name {
		self.ActiveProfile = ""
		for other := range self.Profiles {
			self.ActiveProfile = other
			break
		}
	}
	return nil
}

// ActiveServer is the profile to use, which is the active one unless a name
// was given.
func (self *Config) ActiveServer(name string) (*Profile, error) {
	if name == "" {
		name = self.ActiveProfile
	}
	if name == "" {
		return nil, fmt.Errorf("config: no profile is active, run cf auth login")
	}
	profile, isKnown := self.Profiles[name]
	if !isKnown {
		return nil, fmt.Errorf("config: no profile named %q", name)
	}
	if profile.Domain == "" || profile.Token == "" || profile.Email == "" {
		return nil, fmt.Errorf("config: profile %q is missing a domain, email or token", name)
	}
	profile.Name = name
	return profile, nil
}

// Path is where this tool keeps its own configuration.
func Path() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: finding the configuration directory: %w", err)
	}
	return filepath.Join(directory, "cf", "config.json"), nil
}

// Save writes the configuration, creating its directory if it is not there.
func (self *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(path), err)
	}
	content, err := json.MarshalIndent(self, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	temporary := path + ".tmp"
	// 0600: it holds an API token.
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("config: replacing %s: %w", path, err)
	}
	return nil
}
