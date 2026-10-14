package credentials

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smart-bit-me/scomp-mcp/internal/api"
)

func TestSaveLoadAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	want := Config{
		ServerURL:   "https://link.scomp.me",
		AccessToken: "secret-token",
		Credential:  api.Credential{ID: "id", Label: "laptop", ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.ServerURL != want.ServerURL || got.Credential.Label != "laptop" {
		t.Fatalf("loaded=%+v", got)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed stat=%v", err)
	}
}

func TestEnvironmentTokenClearsStoredCredentialMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := Save(path, Config{
		ServerURL:   "https://link.scomp.me",
		AccessToken: "stored-token",
		Credential: api.Credential{
			ID: "stored-id", Label: "stored laptop",
			Scopes: []api.Scope{api.ScopeTerminalWrite},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvToken, "environment-token")
	config, err := LoadEffective(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.AccessToken != "environment-token" || config.Credential.ID != "" ||
		config.Credential.Label != "" || len(config.Credential.Scopes) != 0 {
		t.Fatalf("effective config exposed stale metadata: %+v", config)
	}
}

func TestSaveDoesNotChangeExistingParentPermissions(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission behavior")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := Save(path, Config{ServerURL: "https://link.scomp.me", AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 {
		t.Fatalf("parent mode changed to %o", info.Mode().Perm())
	}
}
