// Package selfupdate implements npm-style in-place upgrades for the nightshift
// binary. It shells out to the (already-required, already-authed) `gh` CLI to
// query the latest GitHub release and download the GoReleaser archive for the
// current platform, verifies the SHA-256 against the published checksums file,
// untars the `nightshift` binary, and atomically swaps it over the running
// executable. Only the binary is replaced — logs (config dir `logs/` +
// journald) and state (`~/.nightshift-*`) are untouched.
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

const repo = "ahmadAlMezaal/nightshift"

// Latest returns the tag name of the latest GitHub release (e.g. "v0.1.0")
// by shelling out to `gh release view`.
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

// IsNewer reports whether latest is a strictly newer version than current.
// Both are compared as semver-ish major.minor.patch after stripping a leading
// "v" and any pre-release/build suffix. It returns false when current can't be
// compared (empty, "dev", or carries a "-dev"/"-snapshot" suffix) — local and
// development builds never advertise an update.
func IsNewer(latest, current string) bool {
	current = strings.TrimSpace(current)
	if current == "" || current == "dev" {
		return false
	}
	// Development / snapshot builds (e.g. "2.0.0-dev") can't be meaningfully
	// compared to a release tag — don't nag.
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

// parseSemver extracts [major, minor, patch] from a version string, tolerating
// a leading "v" and a trailing pre-release/build suffix. Missing components
// default to 0.
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

// assetName builds the GoReleaser archive filename for the given platform,
// matching the name_template in .goreleaser.yaml:
//
//	{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}{{ if .Arm }}v{{ .Arm }}{{ end }}.tar.gz
//
// GoReleaser's {{ .Version }} is the tag with the leading "v" stripped. For the
// 32-bit arm build GOARM=7 produces an "armv7" suffix; amd64/arm64 have no Arm
// component.
func assetName(version, goos, goarch string) string {
	ver := strings.TrimPrefix(version, "v")
	arch := goarch
	if goarch == "arm" {
		// GOARM is fixed to 7 in .goreleaser.yaml.
		arch = "armv7"
	}
	return fmt.Sprintf("nightshift_%s_%s_%s.tar.gz", ver, goos, arch)
}

const checksumsName = "checksums.txt"

// Update performs the full self-update flow. See package doc.
func Update(ctx context.Context, current string, restart bool) error {
	tag, err := Latest(ctx)
	if err != nil {
		return err
	}

	if !IsNewer(tag, current) {
		fmt.Printf("✓ nightshift is already up to date (%s)\n", displayVersion(current))
		return nil
	}

	fmt.Printf("⬇️  Updating nightshift %s → %s …\n", displayVersion(current), tag)

	asset := assetName(tag, runtime.GOOS, runtime.GOARCH)

	tmp, err := os.MkdirTemp("", "nightshift-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	// Download the platform archive + the checksums file.
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
	fmt.Println("  Restart the service to run the new version:  nightshift logs  /  systemctl --user restart nightshift.service")

	if restart {
		fmt.Println("⟳ Restarting nightshift.service …")
		rs := exec.CommandContext(ctx, "systemctl", "--user", "restart", "nightshift.service")
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

// verifyChecksum confirms the archive's SHA-256 matches the entry for assetName
// in the GoReleaser checksums.txt file (lines of "<hex>  <filename>").
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

// extractBinary reads the `nightshift` binary out of the gzip'd tar archive.
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
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "nightshift" && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read binary from archive: %w", err)
			}
			return data, nil
		}
	}
	return nil, errors.New("nightshift binary not found in archive")
}

// installBinary atomically swaps the new binary over the currently-running
// executable. It resolves os.Executable() through symlinks, writes to a temp
// file in the same directory (so os.Rename is atomic on the same filesystem),
// chmods 0755, and renames into place.
func installBinary(data []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	dir := filepath.Dir(exe)
	tmpf, err := os.CreateTemp(dir, ".nightshift-new-*")
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
