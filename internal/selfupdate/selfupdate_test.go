package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name         string
		latest, curr string
		want         bool
	}{
		{"patch bump", "v0.1.1", "v0.1.0", true},
		{"minor bump", "v0.2.0", "v0.1.9", true},
		{"major bump", "v1.0.0", "v0.9.9", true},
		{"equal", "v0.1.0", "v0.1.0", false},
		{"older latest", "v0.1.0", "v0.2.0", false},
		{"no v prefix both", "1.2.3", "1.2.2", true},
		{"mixed v prefix", "v1.2.3", "1.2.2", true},
		{"dev current", "v0.1.0", "dev", false},
		{"empty current", "v0.1.0", "", false},
		{"snapshot suffix current", "v0.2.0", "2.0.0-dev", false},
		{"snapshot suffix current2", "v0.2.0", "0.1.0-snapshot", false},
		{"prerelease latest still newer", "v0.2.0-rc1", "v0.1.0", true},
		{"two-component versions", "v1.3", "v1.2", true},
		{"two-component equal", "v1.2", "v1.2.0", false},
		{"garbage latest", "not-a-version", "v0.1.0", false},
		{"garbage current", "v0.1.0", "garbage", false},
		{"whitespace", " v0.1.1 ", " v0.1.0 ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsNewer(c.latest, c.curr); got != c.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.curr, got, c.want)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		version, goos, goarch string
		want                  string
	}{
		// .Version strips the leading "v".
		{"v0.1.0", "linux", "amd64", "nightshift_0.1.0_linux_amd64.tar.gz"},
		{"v0.1.0", "linux", "arm64", "nightshift_0.1.0_linux_arm64.tar.gz"},
		{"v0.1.0", "linux", "arm", "nightshift_0.1.0_linux_armv7.tar.gz"},
		{"v0.1.0", "darwin", "amd64", "nightshift_0.1.0_darwin_amd64.tar.gz"},
		{"v0.1.0", "darwin", "arm64", "nightshift_0.1.0_darwin_arm64.tar.gz"},
		{"1.2.3", "linux", "amd64", "nightshift_1.2.3_linux_amd64.tar.gz"},
	}
	for _, c := range cases {
		got := assetName(c.version, c.goos, c.goarch)
		if got != c.want {
			t.Errorf("assetName(%q, %q, %q) = %q, want %q", c.version, c.goos, c.goarch, got, c.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	if v, ok := parseSemver("v1.2.3"); !ok || v != [3]int{1, 2, 3} {
		t.Errorf("parseSemver(v1.2.3) = %v, %v", v, ok)
	}
	if _, ok := parseSemver("1.2.3.4"); ok {
		t.Errorf("parseSemver of 4-component should fail")
	}
	if _, ok := parseSemver(""); ok {
		t.Errorf("parseSemver of empty should fail")
	}
}
