package files

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Migrate copies offline storage into a staged directory on the destination
// filesystem. The caller holds the source process lock and closes SQLite first.
// The source remains intact, including on cancellation or insufficient space.
func Migrate(ctx context.Context, source *Disk, destination, volumeID string, reserve uint64) error {
	if err := source.Check(); err != nil {
		return err
	}
	if err := EmptyDestination(destination); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".sharefile-migrate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = Initialize(stage, volumeID); err != nil {
		return err
	}
	target, err := OpenDisk(stage, volumeID, reserve)
	if err != nil {
		return err
	}
	defer target.Close()
	var size int64
	err = fs.WalkDir(source.Root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("storage contains a symlink or special file: %s", path)
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = target.Space(size); err != nil {
		return err
	}
	buffer := make([]byte, 256<<10)
	err = fs.WalkDir(source.Root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = source.Check(); err != nil {
			return err
		}
		if path == "." || path == ".sharefile-volume" || path == "metadata/process.lock" {
			return nil
		}
		if entry.IsDir() {
			err = target.Root.Mkdir(path, 0700)
			if os.IsExist(err) {
				return nil
			}
			return err
		}
		input, err := source.Root.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("storage file changed: %s", path)
		}
		if err = target.Space(info.Size()); err != nil {
			return err
		}
		output, err := target.Root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.CopyBuffer(output, &contextReader{ctx, input}, buffer)
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	// Persist every directory, including newly created nested object directories.
	err = fs.WalkDir(target.Root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return target.syncDir(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = source.Check(); err != nil {
		return err
	}
	if err = target.Check(); err != nil {
		return err
	}
	if err = EmptyDestination(destination); err != nil {
		return err
	}
	// Darwin does not replace an existing empty directory with Rename.
	if err = os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(stage, destination); err != nil {
		return err
	}
	f, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// EmptyDestination permits a new directory or an existing empty directory only.
func EmptyDestination(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("new storage must be a directory, not a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && err != io.EOF {
		return err
	}
	if len(names) != 0 {
		return fmt.Errorf("new storage directory must be empty")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
