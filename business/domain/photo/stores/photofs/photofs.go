// Package photofs keeps photo files in a directory.
//
// The directory sits beside the database, in the application's own directory
// and never under public_html: deploy.sh refuses an application directory
// Apache could serve, for the database's sake, and the photos inherit that.
// They reach a browser only through the app, which decides who may see an
// unchecked one, and the originals -- GPS and all -- not at all.
package photofs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
)

// Store is a directory of photo files, implementing photobus.Files.
type Store struct {
	dir string
}

var _ photobus.Files = (*Store)(nil)

// NewStore opens the directory, making it if it is not there. 0700, as the
// database is: nobody else on a shared host has a reason to read it.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the photo directory %s: %w", dir, err)
	}

	return &Store{dir: dir}, nil
}

// Put writes a file whole or not at all: into a temporary name, synced, then
// renamed over the real one. A reader never sees half a photo, and a crash
// leaves a stray temporary file rather than a truncated picture.
func (s *Store) Put(name string, data []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	defer os.Remove(tmp.Name()) // a no-op once it has been renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}

	return nil
}

// Open opens a file for reading.
func (s *Store) Open(name string) (photobus.File, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// Remove deletes a file. One that is already gone is not an error: removing
// it is what was asked.
func (s *Store) Remove(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", name, err)
	}

	return nil
}

// path refuses any name that is not a plain file name. Names come from
// photobus, built from an id, so this never fires in use; it is here so that
// it never can, whatever a later caller passes.
func (s *Store) path(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%q is not a photo file name", name)
	}

	return filepath.Join(s.dir, name), nil
}
