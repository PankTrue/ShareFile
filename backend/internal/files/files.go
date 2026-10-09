package files

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"sync"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

type Service struct {
	Store      *metadata.Store
	Disk       *Disk
	ChunkBytes int64
	locks      [64]sync.Mutex
}

func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	m := &s.locks[h.Sum32()%64]
	// Fixed stripes bound memory; cancellation also bounds maintenance waits.
	for !m.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return m.Unlock, nil
}
func (s *Service) Node(ctx context.Context, user, id string) (metadata.Node, error) {
	if err := s.Disk.Check(); err != nil {
		return metadata.Node{}, err
	}
	if err := access.Require(ctx, s.Store.DB, user, id, access.Reader); err != nil {
		return metadata.Node{}, err
	}
	return metadata.GetNode(ctx, s.Store.DB, id)
}
func queryNodes(ctx context.Context, q metadata.Queries, query string, args ...any) ([]metadata.Node, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metadata.Node{}
	for rows.Next() {
		n, e := metadata.ScanNode(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Service) List(ctx context.Context, user, folder string, limit, offset int) ([]metadata.Node, error) {
	if err := s.Disk.Check(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, metadata.ErrInvalid
	}
	if folder == "" {
		return queryNodes(ctx, s.Store.DB, "SELECT "+metadata.NodeColumns+` FROM nodes WHERE kind='folder' AND deleted_at IS NULL AND (
  (parent_id IS NULL AND EXISTS(SELECT 1 FROM users WHERE id=? AND admin=1 AND active=1))
  OR id=(SELECT home_id FROM users WHERE id=? AND active=1)
  OR id IN (SELECT node_id FROM grants WHERE (principal_type='user' AND principal_id=?) OR
  (principal_type='group' AND principal_id IN (SELECT group_id FROM memberships WHERE user_id=?)))
  ) ORDER BY name,id LIMIT ? OFFSET ?`, user, user, user, user, limit, offset)
	}
	if err := access.Require(ctx, s.Store.DB, user, folder, access.Reader); err != nil {
		return nil, err
	}
	n, err := metadata.GetNode(ctx, s.Store.DB, folder)
	if err != nil {
		return nil, err
	}
	if n.Kind != "folder" {
		return nil, metadata.ErrInvalid
	}
	return queryNodes(ctx, s.Store.DB, "SELECT "+metadata.NodeColumns+" FROM nodes WHERE parent_id=? AND deleted_at IS NULL ORDER BY name,id LIMIT ? OFFSET ?", folder, limit, offset)
}
func admin(ctx context.Context, q metadata.Queries, user string) bool {
	var ok bool
	_ = q.QueryRowContext(ctx, "SELECT admin=1 AND active=1 FROM users WHERE id=?", user).Scan(&ok)
	return ok
}
func folderAccess(ctx context.Context, q metadata.Queries, user, folder string, role int) error {
	if err := access.Require(ctx, q, user, folder, role); err != nil {
		return err
	}
	n, err := metadata.GetNode(ctx, q, folder)
	if err != nil {
		return err
	}
	if n.Kind != "folder" {
		return metadata.ErrInvalid
	}
	return nil
}
func (s *Service) CreateFolder(ctx context.Context, user, parent, name string) (metadata.Node, error) {
	return s.CreateFolderOnce(ctx, user, parent, name, "")
}

// CreateFolderOnce recovers a lost response without merging into an existing folder by name.
func (s *Service) CreateFolderOnce(ctx context.Context, user, parent, name, key string) (metadata.Node, error) {
	n := metadata.Node{ID: security.Token(), ParentID: parent, OwnerID: user, Name: name, Kind: "folder", CreatedAt: time.Now().Unix()}
	if !metadata.ValidName(name) || len(key) > 128 {
		return n, metadata.ErrInvalid
	}
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		var parentValue any
		if parent == "" {
			if !admin(ctx, tx, user) {
				return metadata.ErrForbidden
			}
		} else {
			parentValue = parent
			if err := folderAccess(ctx, tx, user, parent, access.Editor); err != nil {
				return err
			}
		}
		if key != "" {
			var id, originalParent, originalName string
			err := tx.QueryRowContext(ctx, "SELECT node_id,parent_id,name FROM folder_requests WHERE user_id=? AND request_key=?", user, key).Scan(&id, &originalParent, &originalName)
			if err == nil {
				if originalParent != parent || originalName != name {
					return metadata.ErrConflict
				}
				existing, err := metadata.GetNode(ctx, tx, id)
				if err != nil {
					return err
				}
				if existing.DeletedAt != nil || existing.ParentID != parent {
					return metadata.ErrConflict
				}
				if err = access.Require(ctx, tx, user, id, access.Editor); err != nil {
					return err
				}
				n = existing
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		unique, err := metadata.UniqueName(ctx, tx, parent, name, "")
		if err != nil {
			return err
		}
		n.Name = unique
		_, err = tx.ExecContext(ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,created_at) VALUES(?,?,?,?,'folder',?)", n.ID, parentValue, user, n.Name, n.CreatedAt)
		if err == nil && key != "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO folder_requests(user_id,request_key,parent_id,name,node_id) VALUES(?,?,?,?,?)", user, key, parent, name, n.ID)
		}
		return err
	})
	return n, err
}
func (s *Service) Move(ctx context.Context, user, id, parent, name string) error {
	if !metadata.ValidName(name) {
		return metadata.ErrInvalid
	}
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		n, err := metadata.GetNode(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = access.Require(ctx, tx, user, id, access.Editor); err != nil {
			return err
		}
		if n.ParentID == "" {
			return metadata.ErrConflict
		}
		if err = folderAccess(ctx, tx, user, parent, access.Editor); err != nil {
			return err
		}
		var cycle int
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE subtree(id) AS (SELECT id FROM nodes WHERE id=? UNION ALL SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id=s.id) SELECT count(*) FROM subtree WHERE id=?`, id, parent).Scan(&cycle)
		if err != nil {
			return err
		}
		if cycle > 0 {
			return metadata.ErrConflict
		}
		var used int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE parent_id=? AND name=? AND deleted_at IS NULL AND id<>?", parent, name, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return metadata.ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE nodes SET parent_id=?,name=? WHERE id=?", parent, name, id); err != nil {
			return err
		}
		return access.RevokeInvalid(ctx, tx)
	})
}
func trashQ(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `WITH RECURSIVE subtree(id) AS (SELECT id FROM nodes WHERE id=? AND deleted_at IS NULL UNION ALL SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id=s.id WHERE n.deleted_at IS NULL)
 UPDATE nodes SET deleted_at=?,trash_root=? WHERE id IN (SELECT id FROM subtree)`, id, time.Now().Unix(), id)
	return err
}
func (s *Service) Delete(ctx context.Context, user, id string) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		if err := access.Require(ctx, tx, user, id, access.Editor); err != nil {
			return err
		}
		var homes int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE home_id=?", id).Scan(&homes); err != nil {
			return err
		}
		if homes > 0 {
			return metadata.ErrConflict
		}
		if err := trashQ(ctx, tx, id); err != nil {
			return err
		}
		return access.RevokeInvalid(ctx, tx)
	})
}
func canTrash(ctx context.Context, q metadata.Queries, user string, n metadata.Node) bool {
	if admin(ctx, q, user) {
		return true
	}
	if n.OwnerID == user {
		return true
	}
	r, e := access.Role(ctx, q, user, n.ParentID)
	return e == nil && r >= access.Manager
}
func (s *Service) Trash(ctx context.Context, user string, limit, offset int) ([]metadata.Node, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, metadata.ErrInvalid
	}
	return queryNodes(ctx, s.Store.DB, `WITH RECURSIVE managed(id) AS (
 SELECT id FROM nodes WHERE deleted_at IS NULL AND (
 (parent_id IS NULL AND owner_id=?) OR id IN (
 SELECT node_id FROM grants WHERE role=3 AND ((principal_type='user' AND principal_id=?) OR
 (principal_type='group' AND principal_id IN (SELECT group_id FROM memberships WHERE user_id=?)))))
 UNION SELECT n.id FROM nodes n JOIN managed m ON n.parent_id=m.id WHERE n.deleted_at IS NULL
 ) SELECT `+metadata.NodeColumns+` FROM nodes WHERE id=trash_root AND (
 owner_id=? OR parent_id IN (SELECT id FROM managed) OR
 EXISTS(SELECT 1 FROM users WHERE id=? AND active=1 AND admin=1))
 ORDER BY deleted_at DESC,id LIMIT ? OFFSET ?`, user, user, user, user, user, limit, offset)
}
func (s *Service) Restore(ctx context.Context, user, id, parent string) (metadata.Node, error) {
	var n metadata.Node
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		var e error
		n, e = metadata.GetNode(ctx, tx, id)
		if e != nil {
			return e
		}
		if n.DeletedAt == nil || n.TrashRoot != n.ID {
			return metadata.ErrConflict
		}
		var purging int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM purges WHERE root_id=?", id).Scan(&purging); e != nil {
			return e
		}
		if purging > 0 {
			return metadata.ErrConflict
		}
		if !canTrash(ctx, tx, user, n) {
			return metadata.ErrForbidden
		}
		if parent == "" {
			parent = n.ParentID
		}
		var parentValue any
		if parent == "" {
			if !admin(ctx, tx, user) {
				return metadata.ErrForbidden
			}
		} else {
			parentValue = parent
			if e = folderAccess(ctx, tx, user, parent, access.Editor); e != nil {
				return e
			}
		}
		n.Name, e = metadata.UniqueName(ctx, tx, parent, n.Name, id)
		if e != nil {
			return e
		}
		n.ParentID = parent
		n.DeletedAt = nil
		n.TrashRoot = ""
		if _, e = tx.ExecContext(ctx, "UPDATE nodes SET parent_id=?,name=? WHERE id=?", parentValue, n.Name, id); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE nodes SET deleted_at=NULL,trash_root=NULL WHERE trash_root=?", id)
		return e
	})
	return n, err
}
func (s *Service) Purge(ctx context.Context, user, id string) error {
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		n, e := metadata.GetNode(ctx, tx, id)
		if e != nil {
			return e
		}
		if n.DeletedAt == nil || n.TrashRoot != id {
			return metadata.ErrConflict
		}
		if user != "" && !canTrash(ctx, tx, user, n) {
			return metadata.ErrForbidden
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO purges VALUES(?) ON CONFLICT DO NOTHING", id)
		return e
	})
	if err != nil {
		return err
	}
	return s.finishPurge(ctx, id)
}
func (s *Service) finishPurge(ctx context.Context, id string) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Disk.Check(); e != nil {
			return e
		}
		var pending int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM purges WHERE root_id=?", id).Scan(&pending); e != nil {
			return e
		}
		if pending == 0 {
			return nil
		}
		// Earlier deletions may leave separate trash roots below this folder.
		// Purging the folder must remove them too, or their parent FK would dangle.
		subtree := `WITH RECURSIVE subtree(id) AS (SELECT id FROM nodes WHERE id=? UNION ALL SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id=s.id) `
		e := metadata.EachID(ctx, tx, subtree+"SELECT id FROM nodes WHERE id IN (SELECT id FROM subtree) AND id>? ORDER BY id LIMIT 128", []any{id}, func(nodeID string) error {
			n, e := metadata.GetNode(ctx, tx, nodeID)
			if e != nil {
				return e
			}
			if n.DeletedAt == nil {
				return metadata.ErrConflict
			}
			if n.Kind == "file" {
				if e = s.Disk.Root.Remove("objects/" + n.ID); e != nil && !errors.Is(e, os.ErrNotExist) {
					return e
				}
			}
			e = metadata.EachID(ctx, tx, "SELECT id FROM uploads WHERE (folder_id=? OR replace_id=?) AND id>? ORDER BY id LIMIT 128", []any{n.ID, n.ID}, func(uploadID string) error {
				u, e := uploadQ(ctx, tx, uploadID)
				if e != nil {
					return e
				}
				if u.State != "complete" {
					if e = s.removePending(u.ID, u.FileID); e != nil {
						return e
					}
				}
				_, e = tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=?", u.ID)
				return e
			})
			if e != nil {
				return e
			}
			for _, query := range []string{"DELETE FROM shares WHERE node_id=?", "DELETE FROM grants WHERE node_id=?"} {
				if _, e = tx.ExecContext(ctx, query, n.ID); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		for _, dir := range []string{"objects", "uploads"} {
			if e = s.Disk.syncDir(dir); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, subtree+"DELETE FROM purges WHERE root_id IN (SELECT id FROM subtree)", id); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, subtree+"DELETE FROM nodes WHERE id IN (SELECT id FROM subtree)", id)
		return e
	})
}
func (s *Service) Open(ctx context.Context, user, id string) (metadata.Node, *os.File, error) {
	n, err := s.Node(ctx, user, id)
	if err != nil {
		return n, nil, err
	}
	if n.Kind != "file" {
		return n, nil, metadata.ErrInvalid
	}
	f, err := s.Disk.Root.Open("objects/" + id)
	if err != nil {
		return n, nil, metadata.ErrUnavailable
	}
	return n, f, nil
}
func (s *Service) uploadTTL(ctx context.Context, q metadata.Queries) time.Duration {
	var seconds int64
	if q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='upload_ttl_seconds'").Scan(&seconds) != nil {
		return 7 * 24 * time.Hour
	}
	return time.Duration(seconds) * time.Second
}
func (s *Service) removePending(id, file string) error {
	for _, path := range []string{"uploads/" + id, "objects/" + file} {
		if err := s.Disk.Root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, dir := range []string{"uploads", "objects"} {
		if err := s.Disk.syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// Import only copies a regular file from a configured source root.
func (s *Service) Import(ctx context.Context, user, folder, name, sourceDir, relative string) (metadata.Node, error) {
	var zero metadata.Node
	root, err := os.OpenRoot(sourceDir)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	fi, err := root.Lstat(relative)
	if err != nil {
		return zero, metadata.ErrInvalid
	}
	if !fi.Mode().IsRegular() {
		return zero, metadata.ErrInvalid
	}
	src, err := root.Open(relative)
	if err != nil {
		return zero, metadata.ErrInvalid
	}
	defer src.Close()
	after, err := src.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(fi, after) {
		return zero, metadata.ErrInvalid
	}
	u, err := s.CreateUpload(ctx, user, folder, name, fi.Size(), "", "")
	if err != nil {
		return zero, err
	}
	for u.Offset < u.Length {
		remaining := min(u.Length-u.Offset, s.ChunkBytes)
		before := u.Offset
		u, err = s.Patch(ctx, user, u.ID, u.Offset, io.LimitReader(src, remaining))
		if err != nil || u.Offset != before+remaining {
			_ = s.Cancel(context.WithoutCancel(ctx), user, u.ID)
			if err == nil {
				err = metadata.ErrConflict
			}
			return zero, err
		}
	}
	return s.Node(ctx, user, u.FileID)
}
