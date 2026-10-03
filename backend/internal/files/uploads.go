package files

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

type Upload struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	FolderID  string `json:"folder_id"`
	Name      string `json:"name"`
	Length    int64  `json:"length"`
	Offset    int64  `json:"offset"`
	State     string `json:"state"`
	FileID    string `json:"file_id"`
	ReplaceID string `json:"replace_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	ExpiresAt int64  `json:"expires_at"`
}

func uploadQ(ctx context.Context, q metadata.Queries, id string) (Upload, error) {
	var u Upload
	var replace sql.NullString
	err := q.QueryRowContext(ctx, "SELECT id,user_id,folder_id,name,length,offset,state,file_id,replace_id,updated_at FROM uploads WHERE id=?", id).Scan(&u.ID, &u.UserID, &u.FolderID, &u.Name, &u.Length, &u.Offset, &u.State, &u.FileID, &replace, &u.UpdatedAt)
	u.ReplaceID = replace.String
	if errors.Is(err, sql.ErrNoRows) {
		err = metadata.ErrNotFound
	}
	return u, err
}
func (s *Service) CreateUpload(ctx context.Context, user, folder, name string, length int64, replace, key string) (Upload, error) {
	u := Upload{ID: security.Token(), UserID: user, FolderID: folder, Name: name, Length: length, State: "open", FileID: security.Token(), ReplaceID: replace, UpdatedAt: time.Now().Unix()}
	if !metadata.ValidName(name) || length < 0 || len(key) > 128 {
		return u, metadata.ErrInvalid
	}
	reused := false
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := folderAccess(ctx, tx, user, folder, access.Editor); err != nil {
			return err
		}
		if key != "" {
			var id string
			e := tx.QueryRowContext(ctx, "SELECT id FROM uploads WHERE user_id=? AND request_key=?", user, key).Scan(&id)
			if e == nil {
				existing, e := uploadQ(ctx, tx, id)
				if e != nil {
					return e
				}
				if existing.FolderID != folder || existing.Name != name || existing.Length != length || existing.ReplaceID != replace {
					return metadata.ErrConflict
				}
				u = existing
				reused = true
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if err := s.Disk.Space(length); err != nil {
			return err
		}
		var repl, keyValue any
		if key != "" {
			keyValue = key
		}
		if replace != "" {
			old, e := metadata.GetNode(ctx, tx, replace)
			if e != nil {
				return e
			}
			if old.Kind != "file" || old.ParentID != folder || old.DeletedAt != nil || old.Name != name {
				return metadata.ErrConflict
			}
			repl = replace
		}
		f, e := s.Disk.Root.OpenFile("uploads/"+u.ID, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		e = f.Sync()
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = s.Disk.syncDir("uploads"); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO uploads(id,user_id,folder_id,name,length,state,file_id,replace_id,updated_at,request_key) VALUES(?,?,?,?,?,'open',?,?,?,?)", u.ID, user, folder, name, length, u.FileID, repl, u.UpdatedAt, keyValue)
		return e
	})
	if err != nil {
		if !reused {
			_ = s.Disk.Root.Remove("uploads/" + u.ID)
		}
		return u, err
	}
	if u.Length == 0 && u.State == "open" {
		u, err = s.Patch(ctx, user, u.ID, 0, emptyReader{})
	}
	u.ExpiresAt = u.UpdatedAt + int64(s.uploadTTL(ctx, s.Store.DB)/time.Second)
	return u, err
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
func (s *Service) Upload(ctx context.Context, user, id string) (Upload, error) {
	if err := s.Disk.Check(); err != nil {
		return Upload{}, err
	}
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Upload{}, lockErr
	}
	defer unlock()
	u, err := uploadQ(ctx, s.Store.DB, id)
	if err != nil {
		return u, err
	}
	if u.UserID != user {
		return u, metadata.ErrNotFound
	}
	if err = folderAccess(ctx, s.Store.DB, user, u.FolderID, access.Editor); err != nil {
		return u, err
	}
	if u.UpdatedAt+int64(s.uploadTTL(ctx, s.Store.DB)/time.Second) <= time.Now().Unix() {
		return u, metadata.ErrNotFound
	}
	if u.State == "committing" {
		if err = s.publish(ctx, u); err != nil {
			return u, err
		}
		u.State = "complete"
	}
	u.ExpiresAt = u.UpdatedAt + int64(s.uploadTTL(ctx, s.Store.DB)/time.Second)
	return u, nil
}
func (s *Service) Patch(ctx context.Context, user, id string, offset int64, body io.Reader) (Upload, error) {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return Upload{}, lockErr
	}
	defer unlock()
	u, err := uploadQ(ctx, s.Store.DB, id)
	if err != nil {
		return u, err
	}
	if u.UserID != user {
		return u, metadata.ErrNotFound
	}
	if err = folderAccess(ctx, s.Store.DB, user, u.FolderID, access.Editor); err != nil {
		return u, err
	}
	if err = s.Disk.Check(); err != nil {
		return u, err
	}
	if u.UpdatedAt+int64(s.uploadTTL(ctx, s.Store.DB)/time.Second) <= time.Now().Unix() {
		return u, metadata.ErrNotFound
	}
	if u.State == "committing" {
		if err = s.publish(ctx, u); err != nil {
			return u, err
		}
		u.State = "complete"
	}
	if offset != u.Offset || u.State == "complete" {
		return u, metadata.ErrConflict
	}
	maxBytes := min(s.ChunkBytes, u.Length-u.Offset)
	if err = s.Disk.Space(maxBytes); err != nil {
		return u, err
	}
	f, err := s.Disk.Root.OpenFile("uploads/"+id, os.O_RDWR, 0600)
	if err != nil {
		return u, metadata.ErrUnavailable
	}
	defer f.Close()
	if err = f.Truncate(u.Offset); err != nil {
		return u, err
	}
	if _, err = f.Seek(u.Offset, io.SeekStart); err != nil {
		return u, err
	}
	n, readErr := io.CopyBuffer(f, io.LimitReader(body, maxBytes+1), make([]byte, 64*1024))
	if n > maxBytes {
		_ = f.Truncate(u.Offset)
		_ = f.Sync()
		return u, metadata.ErrInvalid
	}
	if err = f.Sync(); err != nil {
		_ = f.Truncate(u.Offset)
		return u, err
	}
	oldOffset := u.Offset
	checkpoint, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = s.Store.Write(checkpoint, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		if e := folderAccess(checkpoint, tx, user, u.FolderID, access.Editor); e != nil {
			return e
		}
		current, e := uploadQ(checkpoint, tx, id)
		if e != nil {
			return e
		}
		if current.Offset != oldOffset || current.State != "open" {
			return metadata.ErrConflict
		}
		u.Offset += n
		u.UpdatedAt = time.Now().Unix()
		if u.Offset == u.Length {
			u.State = "committing"
		}
		_, e = tx.ExecContext(checkpoint, "UPDATE uploads SET offset=?,state=?,updated_at=? WHERE id=?", u.Offset, u.State, u.UpdatedAt, id)
		return e
	})
	if err != nil {
		_ = f.Truncate(oldOffset)
		_ = f.Sync()
		u.Offset = oldOffset
		return u, err
	}
	if u.State == "committing" {
		if err = s.publish(checkpoint, u); err != nil {
			return u, err
		}
		u.State = "complete"
	}
	u.ExpiresAt = u.UpdatedAt + int64(s.uploadTTL(checkpoint, s.Store.DB)/time.Second)
	return u, readErr
}
func (s *Service) publish(ctx context.Context, u Upload) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		if e := folderAccess(ctx, tx, u.UserID, u.FolderID, access.Editor); e != nil {
			return e
		}
		current, e := uploadQ(ctx, tx, u.ID)
		if e != nil {
			return e
		}
		if current.State == "complete" {
			return nil
		}
		if current.State != "committing" || current.Offset != current.Length {
			return metadata.ErrConflict
		}
		name := u.Name
		if u.ReplaceID != "" {
			old, e := metadata.GetNode(ctx, tx, u.ReplaceID)
			if e != nil {
				return e
			}
			if old.DeletedAt != nil || old.ParentID != u.FolderID || old.Name != name {
				return metadata.ErrConflict
			}
			if e = trashQ(ctx, tx, old.ID); e != nil {
				return e
			}
			if e = access.RevokeInvalid(ctx, tx); e != nil {
				return e
			}
		} else {
			var e error
			name, e = metadata.UniqueName(ctx, tx, u.FolderID, name, "")
			if e != nil {
				return e
			}
		}
		object := "objects/" + u.FileID
		info, e := s.Disk.Root.Stat(object)
		if errors.Is(e, os.ErrNotExist) {
			if e = s.Disk.Root.Rename("uploads/"+u.ID, object); e != nil {
				return e
			}
			info, e = s.Disk.Root.Stat(object)
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() != u.Length {
			return metadata.ErrUnavailable
		}
		if e = s.Disk.syncDir("objects"); e != nil {
			return e
		}
		if e = s.Disk.syncDir("uploads"); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,size,created_at) VALUES(?,?,?,?,'file',?,?)", u.FileID, u.FolderID, u.UserID, name, u.Length, time.Now().Unix()); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE uploads SET state='complete' WHERE id=?", u.ID)
		return e
	})
}
func (s *Service) Cancel(ctx context.Context, user, id string) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		u, e := uploadQ(ctx, tx, id)
		if e != nil {
			return e
		}
		if u.UserID != user && user != "" {
			return metadata.ErrNotFound
		}
		if u.State == "complete" {
			// Terminating a completed Tus resource keeps the published file.
			_, e = tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", id)
			return e
		}
		if e = s.removePending(u.ID, u.FileID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", id)
		return e
	})
}
func (s *Service) Recover(ctx context.Context) error {
	if err := metadata.EachID(ctx, s.Store.DB, "SELECT root_id FROM purges WHERE root_id>? ORDER BY root_id LIMIT 128", nil, func(id string) error { return s.finishPurge(ctx, id) }); err != nil {
		return err
	}
	if err := metadata.EachID(ctx, s.Store.DB, "SELECT id FROM uploads WHERE state<>'complete' AND id>? ORDER BY id LIMIT 128", nil, func(id string) error { return s.recoverUpload(ctx, id) }); err != nil {
		return err
	}
	// An interrupted create can leave an empty unreferenced upload file.
	dir, err := s.Disk.Root.Open("uploads")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			var count int
			if err = s.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM uploads WHERE id=?", entry.Name()).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				if err = s.Disk.Root.Remove("uploads/" + entry.Name()); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *Service) recoverUpload(ctx context.Context, id string) error {
	u, err := uploadQ(ctx, s.Store.DB, id)
	if err != nil {
		return err
	}
	if u.State == "committing" {
		err = s.publish(ctx, u)
		if errors.Is(err, metadata.ErrForbidden) || errors.Is(err, metadata.ErrNotFound) || errors.Is(err, metadata.ErrConflict) {
			return s.Cancel(ctx, "", id)
		}
		return err
	}
	f, err := s.Disk.Root.OpenFile("uploads/"+id, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return s.Cancel(ctx, "", id)
	}
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.Size() < u.Offset {
		f.Close()
		return s.Cancel(ctx, "", id)
	}
	err = f.Truncate(u.Offset)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	return err
}

func (s *Service) Cleanup(ctx context.Context) error {
	now := time.Now().Unix()
	ttl := int64(s.uploadTTL(ctx, s.Store.DB) / time.Second)
	err := metadata.EachID(ctx, s.Store.DB, "SELECT id FROM uploads WHERE updated_at<=? AND id>? ORDER BY id LIMIT 128", []any{now - ttl}, func(id string) error {
		u, e := uploadQ(ctx, s.Store.DB, id)
		if errors.Is(e, metadata.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if u.State == "complete" {
			_, e = s.Store.DB.ExecContext(ctx, "DELETE FROM uploads WHERE id=? AND state='complete' AND updated_at<=?", id, now-ttl)
		} else {
			e = s.expire(ctx, id, now-ttl)
		}
		if errors.Is(e, metadata.ErrNotFound) {
			return nil
		}
		return e
	})
	if err != nil {
		return err
	}
	err = metadata.EachID(ctx, s.Store.DB, "SELECT id FROM nodes WHERE id=trash_root AND deleted_at<=? AND id>? ORDER BY id LIMIT 128", []any{now - 7*86400}, func(id string) error {
		e := s.Purge(ctx, "", id)
		if errors.Is(e, metadata.ErrNotFound) || errors.Is(e, metadata.ErrConflict) {
			return nil
		}
		return e
	})
	if err != nil {
		return err
	}
	for _, query := range []string{"DELETE FROM sessions WHERE expires_at<=?", "DELETE FROM device_requests WHERE expires_at<=? OR consumed=1", "DELETE FROM share_sessions WHERE expires_at<=?"} {
		if _, err = s.Store.DB.ExecContext(ctx, query, now); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) expire(ctx context.Context, id string, before int64) error {
	unlock, lockErr := s.lock(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		u, e := uploadQ(ctx, tx, id)
		if e != nil {
			return e
		}
		if u.UpdatedAt > before || u.State == "complete" {
			return nil
		}
		if e = s.removePending(u.ID, u.FileID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", id)
		return e
	})
}
