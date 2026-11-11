package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// runRuntimeProjectionCommand copies the files a service's provisioning
// wrote on the host into the service's runtime volume, for the user the
// service runs as. Provisioning keeps its files private (0600, 0700) to the
// user who ran it; a container running as another user cannot read them
// from a bind mount. The projection runs as root in a one-shot container,
// like materialize-credential-issuer, and hands the copies to --owner with
// mode 0400 (directories 0700). Keys stay private: only the owner reads.
func runRuntimeProjectionCommand(arguments []string, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "project-runtime" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	from := set.String("from", "/provisioned", "directory the provisioning wrote, mounted read-only")
	to := set.String("to", "/runtime", "runtime directory of the service, owned by this command")
	owner := set.String("owner", "", "uid or uid:gid the service runs as (required)")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if *owner == "" {
		return true, errors.New("--owner is required")
	}
	o, err := parseFileOwner(*owner)
	if err != nil {
		return true, err
	}
	return true, projectRuntime(*from, *to, o)
}

// projectRuntime copies the regular files under from into to, owned by
// owner. Everything is copied to a staging directory first and replaces the
// contents of to only when complete, so a failed run leaves the previous
// files. A symlink or any other special file is refused.
func projectRuntime(from, to string, owner *fileOwner) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(to, ".staging-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	err = filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == from {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(staging, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(target, 0o700)
		case d.Type().IsRegular():
			return copyPrivateFile(path, target)
		default:
			return fmt.Errorf("%s: only regular files and directories are projected", rel)
		}
	})
	if err != nil {
		return err
	}
	if err := chownTree(staging, *owner); err != nil {
		return fmt.Errorf("hand the runtime files to %d:%d: %w", owner.uid, owner.gid, err)
	}
	return replaceDirectoryContents(to, staging)
}

// copyPrivateFile copies src to dst with mode 0400.
func copyPrivateFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
