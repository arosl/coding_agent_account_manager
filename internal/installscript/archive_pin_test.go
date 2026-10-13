package installscript

// Tests for the arosl fork's --archive-sha256 pins in install.sh (FORK.md).

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pinA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestArchivePinFor(t *testing.T) {
	cases := []struct {
		name string
		args string
		ok   bool
		want string
	}{
		{"no pins", ``, true, ""},
		{"this platform's pin", `--archive-sha256=linux_amd64=` + pinA + ` --archive-sha256=linux_arm64=` + pinB, true, pinA},
		{"two-argument form", `--archive-sha256 linux_arm64=` + pinB + ` --archive-sha256 linux_amd64=` + pinA, true, pinA},
		{"plain pin", `--archive-sha256=` + pinB, true, pinB},
		{"platform pin wins over a plain one", `--archive-sha256=` + pinB + ` --archive-sha256=linux_amd64=` + pinA, true, pinA},
		{"no pin for this platform", `--archive-sha256=linux_arm64=` + pinB, false, ""},
		{"short hex", `--archive-sha256=linux_amd64=abc`, false, ""},
		{"upper-case hex", `--archive-sha256=linux_amd64=` + strings.ToUpper(pinA), false, ""},
		// ACFS spells nhm's platforms with a dash and passes caam's alike.
		{"ACFS's platform spelling", `--archive-sha256 linux-amd64=` + pinA + ` --archive-sha256 linux-arm64=` + pinB, true, pinA},
		{"ACFS's spelling, other platform only", `--archive-sha256 linux-arm64=` + pinB, false, ""},
		{"no separator in the platform", `--archive-sha256=linuxamd64=` + pinA, false, ""},
		{"flag without a value", `--archive-sha256`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runInstallFunc(t, nil, ``, `parse_args `+tc.args+`; archive_pin_for linux_amd64`, "")
			if tc.ok != (code == 0) {
				t.Fatalf("exit %d, want success=%v; stdout=%s stderr=%s", code, tc.ok, stdout, stderr)
			}
			if got := strings.TrimSpace(stdout); got != tc.want {
				t.Errorf("pin = %q, want %q", got, tc.want)
			}
		})
	}
}

// installStubs make try_binary_install download "archive" bytes for a
// linux_amd64 release without the network; verify_release_assets and the
// source build only leave a marker.
const installStubs = `ensure_json_tool() { return 0; }
get_release() { echo '{}'; }
select_release_asset() { printf 'v9.9.9\nhttps://example.test/caam_9.9.9_linux_amd64.tar.gz\ncaam_9.9.9_linux_amd64.tar.gz\n'; }
get_installed_version() { :; }
download_file() { printf 'tampered-archive' > "$2"; }
verify_release_assets() { echo "VERIFY-RAN" >&2; return 0; }
try_go_install() { echo "SOURCE-BUILD" >&2; return 0; }`

// A pinned archive that does not match is refused before anything in the
// release is trusted, and the installer exits instead of building from source.
func TestPinnedArchiveMismatchExits(t *testing.T) {
	setup := installStubs + `
ARCHIVE_PIN=` + sha256Hex("verified-archive")
	stdout, stderr, code := runInstallFunc(t, nil, setup, `try_binary_install linux_amd64; echo "RETURNED $?"`, "")
	if code != 3 {
		t.Fatalf("exit %d, want 3 (verification failed); stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "not its --archive-sha256 pin") {
		t.Errorf("no pin mismatch message; stderr=%s", stderr)
	}
	for _, marker := range []string{"VERIFY-RAN", "SOURCE-BUILD", "RETURNED"} {
		if strings.Contains(stdout+stderr, marker) {
			t.Errorf("%s after a pin mismatch; stdout=%s stderr=%s", marker, stdout, stderr)
		}
	}
}

// A pinned install whose binary path fails never falls back to a source
// build, which cannot match the pin; unmatched pins refuse before any download.
func TestPinnedInstallHasNoSourceFallback(t *testing.T) {
	setup := `detect_platform() { echo linux_amd64; }
try_binary_install() { echo "BINARY-TRIED" >&2; return 1; }
try_go_install() { echo "SOURCE-BUILD" >&2; return 0; }
get_installed_version() { :; }`

	stdout, stderr, code := runInstallFunc(t, nil, setup, `main --archive-sha256 linux_amd64=`+pinA, "")
	if code != 4 || !strings.Contains(stderr, "BINARY-TRIED") || strings.Contains(stderr, "SOURCE-BUILD") {
		t.Fatalf("exit %d, want 4 after the binary attempt and no source build; stdout=%s stderr=%s", code, stdout, stderr)
	}

	stdout, stderr, code = runInstallFunc(t, nil, setup, `main --archive-sha256 linux_arm64=`+pinB, "")
	if code != 3 || strings.Contains(stderr, "BINARY-TRIED") || strings.Contains(stderr, "SOURCE-BUILD") {
		t.Fatalf("exit %d, want 3 before any download; stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Without pins, upstream's source fallback is unchanged.
	stdout, stderr, code = runInstallFunc(t, nil, setup, `main`, "")
	if code != 0 || !strings.Contains(stderr, "SOURCE-BUILD") {
		t.Fatalf("exit %d, want upstream's source build without pins; stdout=%s stderr=%s", code, stdout, stderr)
	}
}

// pathWithout returns a PATH holding only the named tools, so cosign and
// minisign are absent whatever this machine has.
func pathWithout(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A fork release carries a cosign signature only. On a machine without
// cosign (Ubuntu 24.04 packages none), the archive's pin stands in for the
// signature; without a pin the installer still refuses, as upstream's does.
func TestArchivePinStandsInForMissingCosign(t *testing.T) {
	path := pathWithout(t, "python3", "jq", "grep", "awk", "sha256sum", "shasum", "head", "basename", "cat", "sed", "tr", "mktemp", "rm")
	archive := "verified-archive"
	setup := `RELEASE='{"assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"SHA256SUMS.sig","browser_download_url":"https://example.test/SHA256SUMS.sig"}]}'
ARCHIVE="$TMPDIR/caam_9.9.9_linux_amd64.tar.gz"
printf '` + archive + `' > "$ARCHIVE"
download_file() { printf '` + sha256Hex(archive) + `  caam_9.9.9_linux_amd64.tar.gz\n' > "$2"; }`
	body := `verify_release_assets "$RELEASE" v9.9.9 caam_9.9.9_linux_amd64.tar.gz "$ARCHIVE" "$TMPDIR"`

	stdout, stderr, code := runInstallFunc(t, []string{"PATH=" + path}, setup+`
ARCHIVE_PIN=`+sha256Hex(archive), body, "")
	if code != 0 || !strings.Contains(stdout+stderr, "pin stands in for the signature") {
		t.Fatalf("pinned archive without cosign: exit %d; stdout=%s stderr=%s", code, stdout, stderr)
	}

	stdout, stderr, code = runInstallFunc(t, []string{"PATH=" + path}, setup, body, "")
	if code == 0 || !strings.Contains(stdout+stderr, "cosign is required") {
		t.Fatalf("unpinned archive without cosign: exit %d, want cosign is required; stdout=%s stderr=%s", code, stdout, stderr)
	}
}
