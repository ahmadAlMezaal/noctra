package ghauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrNotLinked = errors.New("this Noctra instance is not linked to the GitHub App; run `noctra github login`")

const (
	instanceFile = "instance.json"
	keyFile      = "instance.key"
)

type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

type Instance struct {
	InstanceID    string         `json:"instance_id"`
	ServiceURL    string         `json:"service_url"`
	GitHubUserID  int64          `json:"github_user_id"`
	GitHubLogin   string         `json:"github_login"`
	AppSlug       string         `json:"app_slug"`
	BotLogin      string         `json:"bot_login"`
	BotUserID     int64          `json:"bot_user_id"`
	BotEmail      string         `json:"bot_email"`
	Installations []Installation `json:"installations"`
	LinkedAt      time.Time      `json:"linked_at"`
}

func NewKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func IsLinked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, instanceFile))
	return err == nil
}

func Save(dir string, inst Instance, key ed25519.PrivateKey) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict %s: %w", dir, err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode instance key: %w", err)
	}
	if err := writePrivate(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return err
	}
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, instanceFile), append(data, '\n'))
}

func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.Rename(tmp.Name(), path)
}

func Load(dir string) (Instance, ed25519.PrivateKey, error) {
	var inst Instance
	data, err := os.ReadFile(filepath.Join(dir, instanceFile))
	if errors.Is(err, os.ErrNotExist) {
		return inst, nil, ErrNotLinked
	}
	if err != nil {
		return inst, nil, err
	}
	if err := json.Unmarshal(data, &inst); err != nil {
		return inst, nil, fmt.Errorf("read %s: %w", instanceFile, err)
	}
	if inst.InstanceID == "" || inst.ServiceURL == "" {
		return inst, nil, fmt.Errorf("%s is incomplete; run `noctra github login` again", instanceFile)
	}
	keyPath := filepath.Join(dir, keyFile)
	info, err := os.Stat(keyPath)
	if err != nil {
		return inst, nil, fmt.Errorf("instance key missing; run `noctra github login` again: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return inst, nil, fmt.Errorf("%s is readable by other users (mode %o); run `chmod 600 %s`", keyPath, info.Mode().Perm(), keyPath)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return inst, nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return inst, nil, fmt.Errorf("%s is not a PEM key", keyPath)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return inst, nil, fmt.Errorf("parse %s: %w", keyPath, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return inst, nil, fmt.Errorf("%s is not an Ed25519 key", keyPath)
	}
	return inst, key, nil
}

func Remove(dir string) error {
	var errs []error
	for _, name := range []string{instanceFile, keyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
