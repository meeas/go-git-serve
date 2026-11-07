package bundle

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func selfBytes(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnbundledByDefault(t *testing.T) {
	b, err := Current()
	if b != nil || err != nil {
		t.Fatalf("Current() = %v, %v; want nil, nil", b, err)
	}
	if b, err := Read(selfBytes(t)); b != nil || err != nil {
		t.Fatalf("Read(self) = %v, %v; want nil, nil", b, err)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	bin := selfBytes(t)
	cfg := "[repo]\nurl = 'https://github.com/org/course.git'\n[auth]\ntoken = 'github_pat_SUPERSECRET123'\n"
	out, err := Write(bin, Bundle{Name: "course", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(bin) {
		t.Fatalf("size changed: %d -> %d", len(bin), len(out))
	}
	// (The test binary itself contains these strings as test literals, so
	// look only inside the slot.)
	off, _ := find(out)
	s := out[off : off+SlotSize]
	if bytes.Contains(s, []byte("SUPERSECRET")) || bytes.Contains(s, []byte("github.com/org/course")) {
		t.Error("bundled configuration is readable as plain text")
	}
	if n := bytes.Count(out, magic()); n != 1 {
		t.Errorf("marker appears %d times, want 1", n)
	}
	got, err := Read(out)
	if err != nil || got == nil || got.Name != "course" || got.Config != cfg {
		t.Fatalf("Read = %+v, %v", got, err)
	}

	// Re-bundling replaces the previous bundle.
	out2, err := Write(out, Bundle{Name: "course2", Config: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(out2); got == nil || got.Name != "course2" || got.Config != "x" {
		t.Fatalf("re-bundle: %+v", got)
	}

	// Only the slot changed.
	if !bytes.Equal(bin[:off], out[:off]) || !bytes.Equal(bin[off+SlotSize:], out[off+SlotSize:]) {
		if Platform(bin) != "darwin" { // macOS re-signing legitimately changes hashes
			t.Error("bytes outside the slot changed")
		}
	}
}

func TestDamageDetected(t *testing.T) {
	out, err := Write(selfBytes(t), Bundle{Name: "a", Config: "b"})
	if err != nil {
		t.Fatal(err)
	}
	off, _ := find(out)
	out[off+offPayload] ^= 0xff
	if _, err := Read(out); err == nil {
		t.Fatal("damaged payload not detected")
	}
}

func TestTooLarge(t *testing.T) {
	if _, err := Write(selfBytes(t), Bundle{Name: "a", Config: strings.Repeat("x", Capacity)}); err == nil {
		t.Fatal("oversized configuration accepted")
	}
}

func TestNoSlot(t *testing.T) {
	if _, err := Write([]byte("MZ just some bytes"), Bundle{}); err == nil {
		t.Fatal("expected an error for an executable without a slot")
	}
}

func TestPlatform(t *testing.T) {
	want := map[string]string{"windows": "windows", "linux": "linux", "darwin": "darwin"}[runtime.GOOS]
	if got := Platform(selfBytes(t)); want != "" && got != want {
		t.Errorf("Platform(self) = %s, want %s", got, want)
	}
}

// buildApp cross-compiles the go-git-serve command for goos/goarch.
func buildApp(t *testing.T, goos, goarch string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the application")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not available")
	}
	out := filepath.Join(t.TempDir(), "app")
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s/%s: %v\n%s", goos, goarch, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestResignMatchesGoLinker: recomputing the signature of an unmodified
// Apple Silicon build must reproduce the Go linker's signature exactly.
func TestResignMatchesGoLinker(t *testing.T) {
	orig := buildApp(t, "darwin", "arm64")
	c := bytes.Clone(orig)
	if err := resignMachO(c); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c, orig) {
		t.Fatal("re-signing an unmodified binary changed it: hashes differ from the Go linker's")
	}

	// A bundled build carries a self-consistent signature (macOS itself
	// verifies it in CI).
	out, err := Write(orig, Bundle{Name: "course", Config: "[repo]\nurl='x'\n"})
	if err != nil {
		t.Fatal(err)
	}
	again := bytes.Clone(out)
	if err := resignMachO(again); err != nil || !bytes.Equal(again, out) {
		t.Fatalf("bundled signature not self-consistent: %v", err)
	}
	if bytes.Equal(out, orig) {
		t.Fatal("bundling changed nothing")
	}
	if got, err := Read(out); err != nil || got == nil || got.Name != "course" {
		t.Fatalf("Read(bundled darwin) = %+v, %v", got, err)
	}
}

func TestCrossPlatformBundles(t *testing.T) {
	for _, target := range [][2]string{{"windows", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}} {
		bin := buildApp(t, target[0], target[1])
		if p := Platform(bin); p != target[0] {
			t.Errorf("%s/%s detected as %s", target[0], target[1], p)
		}
		out, err := Write(bin, Bundle{Name: "course", Config: "c"})
		if err != nil {
			t.Fatalf("%s/%s: %v", target[0], target[1], err)
		}
		if got, _ := Read(out); got == nil || got.Config != "c" {
			t.Errorf("%s/%s: round trip failed", target[0], target[1])
		}
	}
}
