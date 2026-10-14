package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/smart-bit-me/scomp-mcp/internal/api"
)

const (
	EnvToken  = "SCOMP_MCP_TOKEN"
	EnvServer = "SCOMP_MCP_SERVER"
)

type Config struct {
	ServerURL   string         `json:"server_url"`
	AccessToken string         `json:"access_token"`
	Credential  api.Credential `json:"credential"`
	SavedAt     time.Time      `json:"saved_at"`
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scomp", "mcp.json"), nil
}

func Load(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if len(data) > 64<<10 {
		return Config{}, errors.New("credential file is too large")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			return Config{}, err
		}
		if info.Mode().Perm()&0077 != 0 {
			return Config{}, fmt.Errorf("credential file %s must not be accessible by group or others", path)
		}
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, errors.New("invalid trailing credential data")
	}
	if config.AccessToken == "" || config.ServerURL == "" {
		return Config{}, errors.New("credential file is incomplete")
	}
	return config, nil
}

func LoadEffective(path string) (Config, error) {
	config, fileErr := Load(path)
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return Config{}, fileErr
	}
	if token := strings.TrimSpace(os.Getenv(EnvToken)); token != "" {
		config.AccessToken = token
		// Metadata in the credential file belongs to its stored token. Do not
		// present stale labels, scopes, or expiry when an environment token
		// overrides that token.
		config.Credential = api.Credential{}
	}
	if server := strings.TrimSpace(os.Getenv(EnvServer)); server != "" {
		config.ServerURL = server
	}
	if config.AccessToken == "" {
		return Config{}, errors.New("not logged in; run scomp-mcp login")
	}
	if config.ServerURL == "" {
		config.ServerURL = api.DefaultServerURL
	}
	return config, nil
}

func Save(path string, config Config) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	if config.AccessToken == "" || config.ServerURL == "" {
		return errors.New("refusing to save incomplete credentials")
	}
	config.SavedAt = time.Now().UTC()
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mcp-credentials-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	if dirHandle, err := os.Open(dir); err == nil {
		err = dirHandle.Sync()
		closeErr := dirHandle.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func Remove(path string) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
