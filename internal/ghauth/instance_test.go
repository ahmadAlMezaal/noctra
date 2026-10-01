package ghauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleInstance() Instance {
	return Instance{
		InstanceID:   "ni_test0000000000000000",
		ServiceURL:   "https://auth.example.test",
		GitHubUserID: 1001,
		GitHubLogin:  "alice",
		AppSlug:      "noctra-agent",
		BotLogin:     "noctra-agent[bot]",
		BotUserID:    336615789,
		BotEmail:     "336615789+noctra-agent[bot]@users.noreply.github.com",
		LinkedAt:     time.Unix(1790000000, 0).UTC(),
	}
}

func TestSaveLoad_RoundTripWithPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "github")
	_, key, _ := NewKey()
	if err := Save(dir, sampleInstance(), key); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{".": 0o700, instanceFile: 0o600, keyFile: 0o600} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", name, got, want)
		}
	}
	inst, loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst.InstanceID != "ni_test0000000000000000" || inst.BotLogin != "noctra-agent[bot]" {
		t.Fatalf("unexpected instance: %+v", inst)
	}
	if !loaded.Equal(key) {
		t.Fatal("key did not round-trip")
	}
	data, _ := os.ReadFile(filepath.Join(dir, instanceFile))
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("instance.json must not contain the key")
	}
	if !IsLinked(dir) {
		t.Fatal("IsLinked should be true after Save")
	}
}

func TestLoad_NotLinked(t *testing.T) {
	if _, _, err := Load(t.TempDir()); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("want ErrNotLinked, got %v", err)
	}
}

func TestLoad_RefusesKeyReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	_, key, _ := NewKey()
	if err := Save(dir, sampleInstance(), key); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("want a permissions error, got %v", err)
	}
}

func TestRemove_DeletesBothFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	_, key, _ := NewKey()
	if err := Save(dir, sampleInstance(), key); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if IsLinked(dir) {
		t.Fatal("still linked after Remove")
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("key file survived Remove")
	}
	if err := Remove(dir); err != nil {
		t.Fatalf("second Remove should be a no-op, got %v", err)
	}
}
