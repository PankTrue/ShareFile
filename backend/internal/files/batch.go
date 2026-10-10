package files

import (
	"context"
	"database/sql"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

const MaxSelection = 100

// Selection checks every requested node, then removes descendants whose parent
// is already selected. A folder and its child must be processed only once.
func (s *Service) Selection(ctx context.Context, user string, ids []string) ([]metadata.Node, error) {
	if err := s.Disk.Check(); err != nil {
		return nil, err
	}
	return selectionQ(ctx, s.Store.DB, user, ids, access.Reader)
}

func selectionQ(ctx context.Context, q metadata.Queries, user string, ids []string, role int) ([]metadata.Node, error) {
	if len(ids) == 0 || len(ids) > MaxSelection {
		return nil, metadata.ErrInvalid
	}
	selected := make(map[string]bool, len(ids))
	nodes := make([]metadata.Node, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 128 || selected[id] {
			return nil, metadata.ErrInvalid
		}
		selected[id] = true
		if err := access.Require(ctx, q, user, id, role); err != nil {
			return nil, err
		}
		n, err := metadata.GetNode(ctx, q, id)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	roots := make([]metadata.Node, 0, len(nodes))
	for _, n := range nodes {
		rows, err := q.QueryContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT id,parent_id FROM nodes WHERE id=? AND deleted_at IS NULL
 UNION ALL SELECT n.id,n.parent_id FROM nodes n JOIN ancestors a ON n.id=a.parent_id WHERE n.deleted_at IS NULL
 ) SELECT id FROM ancestors WHERE id<>?`, n.ID, n.ID)
		if err != nil {
			return nil, err
		}
		covered := false
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			covered = covered || selected[id]
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if !covered {
			roots = append(roots, n)
		}
	}
	return roots, nil
}

// Batch changes metadata in one transaction, including access/link revocation.
// Collect moves the selection into a new folder that the caller can share.
func (s *Service) Batch(ctx context.Context, user, action string, ids []string, parent, name string) (metadata.Node, error) {
	var folder metadata.Node
	if action != "move" && action != "delete" && action != "collect" {
		return folder, metadata.ErrInvalid
	}
	if action == "collect" && !metadata.ValidName(name) {
		return folder, metadata.ErrInvalid
	}
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		nodes, err := selectionQ(ctx, tx, user, ids, access.Editor)
		if err != nil {
			return err
		}
		if action == "collect" {
			if err = folderAccess(ctx, tx, user, parent, access.Manager); err != nil {
				return err
			}
			unique, err := metadata.UniqueName(ctx, tx, parent, name, "")
			if err != nil {
				return err
			}
			folder = metadata.Node{ID: security.Token(), ParentID: parent, OwnerID: user, Name: unique, Kind: "folder", CreatedAt: time.Now().Unix()}
			if _, err = tx.ExecContext(ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,created_at) VALUES(?,?,?,?,'folder',?)", folder.ID, parent, user, folder.Name, folder.CreatedAt); err != nil {
				return err
			}
			parent = folder.ID
		}
		for _, n := range nodes {
			if action == "delete" {
				err = deleteQ(ctx, tx, user, n.ID)
			} else {
				nodeName := n.Name
				if action == "collect" {
					nodeName, err = metadata.UniqueName(ctx, tx, parent, n.Name, n.ID)
					if err != nil {
						return err
					}
				}
				err = moveQ(ctx, tx, user, n.ID, parent, nodeName)
			}
			if err != nil {
				return err
			}
		}
		return access.RevokeInvalid(ctx, tx)
	})
	return folder, err
}
