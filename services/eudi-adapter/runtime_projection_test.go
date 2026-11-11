package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Review finding 1: the provisioned files of a service are private to the
// user who provisioned them on the host; the projection copies them into
// the service's runtime volume for the user the service runs as.
func TestProjectRuntime(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(from, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config.json", `{"api_key_file":"api-key"}`)
	write("api-key", "secret")
	write("pid-trust/anchors.pem", "anchors")
	if err := os.WriteFile(filepath.Join(to, "stale"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := &fileOwner{uid: os.Getuid(), gid: os.Getgid()}
	if err := projectRuntime(from, to, owner); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"config.json": `{"api_key_file":"api-key"}`, "api-key": "secret", "pid-trust/anchors.pem": "anchors"} {
		p := filepath.Join(to, rel)
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q %v, want %q", rel, got, err, want)
		}
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o400 {
			t.Errorf("%s mode = %v %v, want 0400", rel, info.Mode().Perm(), err)
		}
	}
	if info, err := os.Stat(filepath.Join(to, "pid-trust")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pid-trust mode = %v %v, want 0700", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(to, "stale")); !os.IsNotExist(err) {
		t.Error("the projection must replace what was there")
	}
	entries, _ := os.ReadDir(to)
	if len(entries) != 3 {
		t.Errorf("runtime directory holds %d entries, want 3 (no staging left)", len(entries))
	}
}

func TestProjectRuntimeRefusesSymlinksAndKeepsThePrevious(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(from, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "config.json"), []byte("previous"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := projectRuntime(from, to, &fileOwner{uid: os.Getuid(), gid: os.Getgid()}); err == nil {
		t.Fatal("a symlink must be refused")
	}
	if got, _ := os.ReadFile(filepath.Join(to, "config.json")); string(got) != "previous" {
		t.Errorf("a failed projection must keep the previous files, got %q", got)
	}
}

func TestProjectRuntimeCommandNeedsAnOwner(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if handled, err := runRuntimeProjectionCommand([]string{"project-runtime", "--from", from, "--to", to}, os.Stderr); !handled || err == nil {
		t.Errorf("without --owner = %v %v, want an error", handled, err)
	}
	if handled, err := runRuntimeProjectionCommand([]string{"project-runtime", "--from", from, "--to", to, "--owner", strconv.Itoa(os.Getuid())}, os.Stderr); !handled || err != nil {
		t.Errorf("with --owner = %v %v", handled, err)
	}
	if handled, _ := runRuntimeProjectionCommand([]string{"something-else"}, os.Stderr); handled {
		t.Error("another command is not this one")
	}
}
