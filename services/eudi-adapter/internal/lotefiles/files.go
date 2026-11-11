// Package lotefiles adapts public CA files and generation-based publication
// to the trust list use case. No signer key is written into the public tree.
package lotefiles

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/trustlists"
	"golang.org/x/sys/unix"
)

type Source struct {
	Roots map[trustlists.Role]string
	NL    trustlists.Source
}

func (s Source) Load(ctx context.Context) (trustlists.Snapshot, error) {
	// Local input errors are discovered before the network request.
	roots := map[trustlists.Role][]byte{}
	for _, role := range trustlists.Roles {
		der, err := ReadCertificate(s.Roots[role])
		if err != nil {
			return trustlists.Snapshot{}, fmt.Errorf("%s root: %w", role, err)
		}
		roots[role] = der
	}
	snapshot, err := s.NL.Load(ctx)
	if err != nil {
		return snapshot, err
	}
	snapshot.GBO = roots
	return snapshot, nil
}

func ReadCertificate(path string) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("expected exactly one CERTIFICATE PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert.Raw, nil
}

type Publications struct{ Directory string }

func (s Publications) Update(ctx context.Context, update func(*trustlists.Publication) (*trustlists.Publication, error)) error {
	if err := os.MkdirAll(s.Directory, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.Directory, ".lote.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	// Closing the lock file releases the lock as well.
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	previous, err := s.current()
	if err != nil {
		return err
	}
	next, err := update(previous)
	if err != nil || next == nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.publish(next)
}

func (s Publications) current() (*trustlists.Publication, error) {
	current := filepath.Join(s.Directory, "lote")
	_, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	link, err := os.Readlink(current)
	if err != nil {
		return nil, errors.New("lote must be a managed publication symlink; refusing to replace an existing directory")
	}
	if filepath.IsAbs(link) || filepath.Dir(link) != ".lote-generations" {
		return nil, errors.New("lote points outside the managed generations")
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(s.Directory, ".lote-generations"))
	if err != nil {
		return nil, err
	}
	if filepath.Dir(resolved) != parent {
		return nil, errors.New("publication resolves outside the managed generations")
	}
	body, err := os.ReadFile(filepath.Join(current, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var p trustlists.Publication
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if p.Sequence == 0 || p.Digest == "" || p.NLVersion == 0 || len(p.Files) != len(trustlists.Roles) || !p.NextUpdate.After(p.Issue) {
		return nil, errors.New("invalid previous publication; refusing to reset its sequence")
	}
	for _, role := range trustlists.Roles {
		raw, err := os.ReadFile(filepath.Join(current, role.Filename()))
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 || !bytes.Equal(raw, p.Files[role.Filename()]) {
			return nil, errors.New("previous publication does not match its manifest")
		}
	}
	return &p, nil
}

func (s Publications) publish(p *trustlists.Publication) error {
	if len(p.Files) != len(trustlists.Roles) {
		return errors.New("publication must contain exactly four role lists")
	}
	parent := filepath.Join(s.Directory, ".lote-generations")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	generation, err := os.MkdirTemp(parent, "set-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(generation)
		}
	}()
	if err := os.Chmod(generation, 0o755); err != nil {
		return err
	}
	write := func(name string, body []byte) error {
		f, err := os.OpenFile(filepath.Join(generation, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(body)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		return errors.Join(writeErr, closeErr)
	}
	for _, role := range trustlists.Roles {
		body := p.Files[role.Filename()]
		if len(body) == 0 {
			return fmt.Errorf("missing %s", role.Filename())
		}
		if err := write(role.Filename(), body); err != nil {
			return err
		}
	}
	manifest, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := write("manifest.json", manifest); err != nil {
		return err
	}
	// The generation exists completely before the relative pointer is swapped.
	link := generation + ".link"
	if err := os.Symlink(filepath.Join(".lote-generations", filepath.Base(generation)), link); err != nil {
		return err
	}
	// After a successful rename the link no longer exists under this name.
	defer func() { _ = os.Remove(link) }()
	if err := os.Rename(link, filepath.Join(s.Directory, "lote")); err != nil {
		return err
	}
	committed = true
	return nil
}
