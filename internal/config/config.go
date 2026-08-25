// Package config owns skout's private, Rust-compatible user configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Config contains the persisted skout preferences.
type Config struct {
	CurrentLeague      string `json:"current_league,omitempty"`
	CurrentTeamKey     string `json:"current_team_key,omitempty"`
	PullPublicLeagueID string `json:"pull_public_league_id,omitempty"`
}

// MarshalJSON preserves the frozen writer contract by omitting the deprecated
// public-league field while still allowing old files to be read.
func (config Config) MarshalJSON() ([]byte, error) {
	type persisted struct {
		CurrentLeague  string `json:"current_league,omitempty"`
		CurrentTeamKey string `json:"current_team_key,omitempty"`
	}
	return json.Marshal(persisted{
		CurrentLeague:  config.CurrentLeague,
		CurrentTeamKey: config.CurrentTeamKey,
	})
}

// Path resolves the production configuration path without creating it.
func Path() (string, error) {
	home, ok := os.LookupEnv("HOME")
	if !ok || home == "" {
		return "", fmt.Errorf("resolve configuration path skout: HOME is unavailable; set HOME to the user home directory and retry")
	}
	return filepath.Join(home, ".config", "skout", "config.json"), nil
}

// Read reads the production configuration.
func Read() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	return ReadAt(path)
}

// ReadAt reads an explicit path; absence yields the empty configuration.
func ReadAt(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, pathError("read configuration", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, pathError("parse configuration", path, errors.New("configuration JSON is malformed"))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, pathError("parse configuration", path, errors.New("configuration JSON is malformed"))
	}
	return config, nil
}

// Write writes the production configuration atomically.
func Write(config Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	return WriteAt(path, config)
}

// WriteAt atomically replaces an explicit private configuration path.
func WriteAt(path string, config Config) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return pathError("write configuration", path, errors.New("path has no parent"))
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return pathError("create configuration directory", path, err)
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pathError("inspect configuration", path, err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return pathError("serialize configuration", path, errors.New("configuration is invalid"))
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(parent, ".config-*.tmp")
	if err != nil {
		return pathError("create temporary configuration", path, err)
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err := temporary.Chmod(mode); err != nil {
		cleanup()
		return pathError("set temporary configuration permissions", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return pathError("write temporary configuration", path, err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return pathError("sync temporary configuration", path, err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return pathError("close temporary configuration", path, err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Remove(temporaryName)
		return pathError("replace configuration", path, err)
	}
	return nil
}

func pathError(operation, path string, err error) error {
	return fmt.Errorf("%s %s: %v; check the path and permissions, then retry", operation, path, err)
}
