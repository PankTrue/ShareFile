// Package maintenance provides offline, non-overwriting backup and restore.
package maintenance

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sharefile/backend/internal/files"
	"sharefile/backend/internal/metadata"
)

var idPattern = regexp.MustCompile("^[A-Za-z0-9_-]{43}$")

func permitted(name string) bool {
	if name == ".sharefile-volume" || name == "metadata/sharefile.db" {
		return true
	}
	parts := strings.Split(name, "/")
	return len(parts) == 2 && (parts[0] == "objects" || parts[0] == "uploads") && idPattern.MatchString(parts[1])
}
func Backup(ctx context.Context, disk *files.Disk, path string) (result error) {
	if !filepath.IsAbs(path) {
		return metadata.ErrInvalid
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	storage, err := filepath.EvalSymlinks(disk.Path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(storage, filepath.Join(parent, filepath.Base(path)))
	if err != nil || filepath.IsLocal(rel) {
		return fmt.Errorf("backup must be outside storage")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		if result != nil {
			_ = os.Remove(path)
		}
	}()
	writer := tar.NewWriter(f)
	for _, name := range []string{".sharefile-volume", "metadata/sharefile.db"} {
		if err = archiveFile(ctx, disk, writer, name); err != nil {
			return err
		}
	}
	for _, dirName := range []string{"objects", "uploads"} {
		dir, err := disk.Root.Open(dirName)
		if err != nil {
			return err
		}
		for {
			entries, readErr := dir.ReadDir(128)
			for _, entry := range entries {
				if err = archiveFile(ctx, disk, writer, dirName+"/"+entry.Name()); err != nil {
					dir.Close()
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				dir.Close()
				return readErr
			}
		}
		dir.Close()
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return f.Sync()
}

func archiveFile(ctx context.Context, disk *files.Disk, writer *tar.Writer, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := disk.Check(); err != nil {
		return err
	}
	if !permitted(name) {
		return fmt.Errorf("unexpected storage entry")
	}
	info, err := disk.Root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("regular files required")
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	header.Mode = 0600
	if err = writer.WriteHeader(header); err != nil {
		return err
	}
	src, err := disk.Root.Open(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, &contextReader{ctx, src})
	src.Close()
	if err != nil {
		return err
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Restore is allowed only into a new directory under an existing parent.
// A failed restore is retained for inspection and must not be used as live storage.
func Restore(ctx context.Context, dir, volume, archive string) error {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(archive) {
		return metadata.ErrInvalid
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore destination must not exist")
	}
	src, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer src.Close()
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"metadata", "uploads", "objects"} {
		if err = root.Mkdir(name, 0700); err != nil {
			return err
		}
	}
	reader := tar.NewReader(src)
	markerSeen, databaseSeen := false, false
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !permitted(h.Name) || h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsafe or duplicate archive entry")
		}
		markerSeen = markerSeen || h.Name == ".sharefile-volume"
		databaseSeen = databaseSeen || h.Name == "metadata/sharefile.db"
		dest, err := root.OpenFile(h.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(dest, &contextReader{ctx, reader})
		if err == nil {
			err = dest.Sync()
		}
		dest.Close()
		if err != nil {
			return err
		}
	}
	if !markerSeen || !databaseSeen {
		return fmt.Errorf("incomplete archive")
	}
	marker, err := root.ReadFile(".sharefile-volume")
	if err != nil || string(marker) != volume+"\n" {
		return fmt.Errorf("archive volume_id mismatch")
	}
	store, err := metadata.Open(filepath.Join(dir, "metadata", "sharefile.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	var check string
	if err = store.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		return fmt.Errorf("metadata integrity check failed")
	}
	var violations int
	if err = store.DB.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		return fmt.Errorf("metadata foreign key check failed")
	}
	// DB values also become file paths during recovery; tar names alone are not enough.
	for _, query := range []string{"SELECT id FROM nodes", "SELECT id FROM uploads", "SELECT file_id FROM uploads"} {
		rows, e := store.DB.QueryContext(ctx, query)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			if !idPattern.MatchString(id) {
				rows.Close()
				return fmt.Errorf("unsafe metadata identifier")
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	disk, err := files.OpenDisk(dir, volume, 0)
	if err != nil {
		return err
	}
	defer disk.Close()
	fs := &files.Service{Store: store, Disk: disk, ChunkBytes: 16 << 20}
	if err = fs.Recover(ctx); err != nil {
		return err
	}
	rows, err := store.DB.QueryContext(ctx, "SELECT id,size FROM nodes WHERE kind='file'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var size int64
		if err = rows.Scan(&id, &size); err != nil {
			rows.Close()
			return err
		}
		if !idPattern.MatchString(id) {
			rows.Close()
			return fmt.Errorf("invalid object identifier")
		}
		info, e := root.Stat("objects/" + id)
		if e != nil || !info.Mode().IsRegular() || info.Size() != size {
			rows.Close()
			return fmt.Errorf("missing or invalid object")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return nil
}
