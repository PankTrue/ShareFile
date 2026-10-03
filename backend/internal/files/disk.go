package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"sharefile/backend/internal/metadata"
)

type Disk struct {
	Root           *os.Root
	Path, VolumeID string
	info           os.FileInfo
	Reserve        uint64
}

// Initialize requires an existing parent directory. It never formats a device.
func Initialize(dir, id string) error {
	if !filepath.IsAbs(dir) || id == "" || strings.ContainsAny(id, "\r\n") {
		return metadata.ErrInvalid
	}
	parent, err := os.Stat(filepath.Dir(dir))
	if err != nil || !parent.IsDir() {
		return fmt.Errorf("storage parent must exist")
	}
	if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return metadata.ErrInvalid
	}
	marker := filepath.Join(dir, ".sharefile-volume")
	if markerInfo, e := os.Lstat(marker); e == nil {
		if !markerInfo.Mode().IsRegular() {
			return metadata.ErrConflict
		}
	} else if errors.Is(e, os.ErrNotExist) {
		folder, e := os.Open(dir)
		if e != nil {
			return e
		}
		entries, readErr := folder.ReadDir(1)
		folder.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("uninitialized storage directory must be empty")
		}
	} else {
		return e
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, e := os.ReadFile(marker)
		if e != nil {
			return e
		}
		if string(existing) != id+"\n" {
			return metadata.ErrConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(id + "\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, path := range []string{dir, filepath.Dir(dir)} {
		folder, e := os.Open(path)
		if e != nil {
			return e
		}
		e = folder.Sync()
		folder.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func OpenDisk(dir, id string, reserve uint64) (*Disk, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	d := &Disk{root, dir, id, info, reserve}
	if err = d.Check(); err != nil {
		root.Close()
		return nil, err
	}
	for _, name := range []string{"objects", "uploads", "metadata"} {
		if err = root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			root.Close()
			return nil, err
		}
		fi, e := root.Lstat(name)
		if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, metadata.ErrUnavailable
		}
	}
	return d, nil
}
func (d *Disk) Check() error {
	current, err := os.Stat(d.Path)
	if err != nil || !os.SameFile(current, d.info) {
		return metadata.ErrUnavailable
	}
	marker, err := d.Root.ReadFile(".sharefile-volume")
	if err != nil || string(marker) != d.VolumeID+"\n" {
		return metadata.ErrUnavailable
	}
	return nil
}
func (d *Disk) Space(bytes int64) error {
	if err := d.Check(); err != nil {
		return err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(d.Path, &st); err != nil {
		return metadata.ErrUnavailable
	}
	available := uint64(st.Bavail) * uint64(st.Bsize)
	if bytes < 0 || available < d.Reserve || uint64(bytes) > available-d.Reserve {
		return metadata.ErrSpace
	}
	return nil
}
func (d *Disk) syncDir(name string) error {
	f, err := d.Root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (d *Disk) Close() error { return d.Root.Close() }
