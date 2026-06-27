// Package selfupdate does in-place binary upgrades: via `gh` it downloads the latest GoReleaser archive
// for this platform, verifies its SHA-256, and atomically swaps the binary; logs and state are untouched.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const repo = "ahmadAlMezaal/noctra"

// Latest returns the latest GitHub release tag (e.g. "v0.1.0") via `gh release view`.
func Latest(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "release", "view",
		"--repo", repo, "--json", "tagName")
	out, err := cmd.Output()
	if err != nil {
		if ee := (&exec.ExitError{}); errors.As(err, &ee) {
			return "", fmt.Errorf("gh release view: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("gh release view: %w", err)
	}
	var res struct {
		TagName string `json:"tagName"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("parse gh output: %w", err)
	}
	if res.TagName == "" {
		return "", errors.New("no tagName in latest release")
	}
	return res.TagName, nil
}

// IsNewer reports whether latest is strictly newer than current (compared as major.minor.patch);
// false when current is empty/"dev"/a "-dev"/"-snapshot" build, so dev builds never advertise updates.
func IsNewer(latest, current string) bool {
	current = strings.TrimSpace(current)
	if current == "" || current == "dev" {
		return false
	}
	// Dev/snapshot builds can't be compared to a release tag — don't nag.
	if i := strings.IndexAny(current, "-+"); i >= 0 {
		if suf := current[i:]; strings.Contains(suf, "dev") || strings.Contains(suf, "snapshot") {
			return false
		}
	}
	lv, ok1 := parseSemver(latest)
	cv, ok2 := parseSemver(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if lv[i] != cv[i] {
			return lv[i] > cv[i]
		}
	}
	return false
}

// parseSemver extracts [major, minor, patch] from a version, tolerating a leading "v" and a suffix; missing parts default to 0.
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return out, false
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// assetName builds the GoReleaser archive filename, matching the name_template in .goreleaser.yaml
// (tag's leading "v" stripped; GOARM=7 → "armv7" suffix, amd64/arm64 have no Arm component):
//
//	{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}{{ if .Arm }}v{{ .Arm }}{{ end }}.tar.gz
func assetName(version, goos, goarch string) string {
	ver := strings.TrimPrefix(version, "v")
	arch := goarch
	if goarch == "arm" {
		arch = "armv7" // GOARM is fixed to 7 in .goreleaser.yaml.
	}
	return fmt.Sprintf("noctra_%s_%s_%s.tar.gz", ver, goos, arch)
}

const checksumsName = "checksums.txt"

// Update performs the full self-update flow. See package doc.
func Update(ctx context.Context, current string, restart bool) error {
	tag, err := Latest(ctx)
	if err != nil {
		return err
	}

	if !IsNewer(tag, current) {
		fmt.Printf("✓ noctra is already up to date (%s)\n", displayVersion(current))
		return nil
	}

	fmt.Printf("⬇️  Updating noctra %s → %s …\n", displayVersion(current), tag)

	asset := assetName(tag, runtime.GOOS, runtime.GOARCH)

	tmp, err := os.MkdirTemp("", "noctra-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	dl := exec.CommandContext(ctx, "gh", "release", "download", tag,
		"--repo", repo,
		"--pattern", asset,
		"--pattern", checksumsName,
		"--dir", tmp)
	dl.Stderr = os.Stderr
	if err := dl.Run(); err != nil {
		return fmt.Errorf("download release %s asset %q: %w", tag, asset, err)
	}

	archivePath := filepath.Join(tmp, asset)
	if err := verifyChecksum(archivePath, filepath.Join(tmp, checksumsName), asset); err != nil {
		return err
	}

	binData, err := extractBinary(archivePath)
	if err != nil {
		return err
	}

	if err := installBinary(binData); err != nil {
		return err
	}

	fmt.Printf("✓ Updated to %s\n", tag)
	fmt.Println("  Restart the service to run the new version:  noctra logs  /  systemctl --user restart noctra.service")

	if restart {
		fmt.Println("⟳ Restarting noctra.service …")
		rs := exec.CommandContext(ctx, "systemctl", "--user", "restart", "noctra.service")
		rs.Stdout = os.Stdout
		rs.Stderr = os.Stderr
		if err := rs.Run(); err != nil {
			// Best-effort: not every host runs under systemd.
			fmt.Fprintf(os.Stderr, "⚠️  could not restart service automatically (%v) — restart it manually\n", err)
		}
	}
	return nil
}

func displayVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// verifyChecksum confirms the archive's SHA-256 matches assetName's entry in checksums.txt ("<hex>  <filename>" lines).
func verifyChecksum(archivePath, checksumsPath, assetName string) error {
	data, err := os.ReadFile(checksumsPath)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	var want string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %q in %s", assetName, checksumsName)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}
	return nil
}

// extractBinary reads the `noctra` binary out of the gzip'd tar archive.
func extractBinary(archivePath string) ([]byte, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "noctra" && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read binary from archive: %w", err)
			}
			return data, nil
		}
	}
	return nil, errors.New("noctra binary not found in archive")
}

// installBinary atomically swaps the new binary over the running exe: write to a same-dir temp (keeps os.Rename atomic), chmod 0755, rename.
func installBinary(data []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	dir := filepath.Dir(exe)
	tmpf, err := os.CreateTemp(dir, ".noctra-new-*")
	if err != nil {
		return permHint(err, dir)
	}
	tmpName := tmpf.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpf.Write(data); err != nil {
		tmpf.Close()
		return err
	}
	if err := tmpf.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	if err := os.Rename(tmpName, exe); err != nil {
		return permHint(err, exe)
	}
	cleanup = false
	return nil
}

// permHint wraps permission-denied errors with an actionable message.
func permHint(err error, path string) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("binary not writable at %s — reinstall or re-run with sudo: %w", path, err)
	}
	return err
}
