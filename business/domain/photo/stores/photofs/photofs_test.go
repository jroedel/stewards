package photofs_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
)

func TestAFileIsKeptPrivatelyAndWhole(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "photo-files")

	s, err := photofs.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v, %v", st.Mode().Perm(), err)
	}

	if err := s.Put("abc-large.jpg", []byte("first")); err != nil {
		t.Fatal(err)
	}

	if err := s.Put("abc-large.jpg", []byte("second")); err != nil {
		t.Fatal(err)
	}

	f, err := s.Open("abc-large.jpg")
	if err != nil {
		t.Fatal(err)
	}

	b, _ := io.ReadAll(f)
	f.Close()

	if string(b) != "second" {
		t.Errorf("read %q", b)
	}

	// Nothing but the file: no temporary left behind.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the directory holds %d entries", len(entries))
	}

	for range 2 { // the second time, it is already gone
		if err := s.Remove("abc-large.jpg"); err != nil {
			t.Errorf("remove: %v", err)
		}
	}
}

// A name is a file name and nothing else, whatever a caller passes.
func TestANameCannotLeaveTheDirectory(t *testing.T) {
	s, err := photofs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", "../config.toml", "a/b.jpg", ".hidden", "..", "/etc/passwd"} {
		if err := s.Put(name, []byte("x")); err == nil {
			t.Errorf("Put %q was allowed", name)
		}

		if _, err := s.Open(name); err == nil {
			t.Errorf("Open %q was allowed", name)
		}

		if err := s.Remove(name); err == nil {
			t.Errorf("Remove %q was allowed", name)
		}
	}
}
