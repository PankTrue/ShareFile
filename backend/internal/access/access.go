// Package access is the single decision point for inherited permissions.
package access

import (
	"context"
	"database/sql"
	"errors"

	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

const (
	Reader  = 1
	Editor  = 2
	Manager = 3
)

type Service struct{ Store *metadata.Store }

func Role(ctx context.Context, q metadata.Queries, user, node string) (int, error) {
	n, err := metadata.GetNode(ctx, q, node)
	if err != nil {
		return 0, err
	}
	if n.DeletedAt != nil {
		return 0, metadata.ErrNotFound
	}
	var active, admin int
	err = q.QueryRowContext(ctx, "SELECT active,admin FROM users WHERE id=?", user).Scan(&active, &admin)
	if errors.Is(err, sql.ErrNoRows) || active == 0 {
		return 0, metadata.ErrForbidden
	}
	if err != nil {
		return 0, err
	}
	if admin == 1 {
		return Manager, nil
	}
	var role int
	err = q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent_id,owner_id) AS (
 SELECT id,parent_id,owner_id FROM nodes WHERE id=? AND deleted_at IS NULL
 UNION ALL SELECT n.id,n.parent_id,n.owner_id FROM nodes n JOIN ancestors a ON n.id=a.parent_id WHERE n.deleted_at IS NULL
 ) SELECT coalesce(max(role),0) FROM (
 SELECT 3 AS role FROM ancestors WHERE parent_id IS NULL AND owner_id=?
 UNION ALL SELECT g.role FROM grants g JOIN ancestors a ON a.id=g.node_id
 WHERE (g.principal_type='user' AND g.principal_id=?) OR
 (g.principal_type='group' AND g.principal_id IN (SELECT group_id FROM memberships WHERE user_id=?))
 )`, node, user, user, user).Scan(&role)
	return role, err
}
func Require(ctx context.Context, q metadata.Queries, user, node string, min int) error {
	role, err := Role(ctx, q, user, node)
	if err != nil {
		return err
	}
	if role < min {
		return metadata.ErrForbidden
	}
	return nil
}

// RevokeInvalid makes revocation persistent, so restoring rights cannot revive links.
// Call inside the transaction that changes access, node ancestry, or account status.
func RevokeInvalid(ctx context.Context, tx *sql.Tx) error {
	return metadata.EachID(ctx, tx, "SELECT id FROM shares WHERE revoked=0 AND id>? ORDER BY id LIMIT 128", nil, func(id string) error {
		var user, node string
		if err := tx.QueryRowContext(ctx, "SELECT user_id,node_id FROM shares WHERE id=?", id).Scan(&user, &node); err != nil {
			return err
		}
		role, e := Role(ctx, tx, user, node)
		if e != nil && !errors.Is(e, metadata.ErrNotFound) && !errors.Is(e, metadata.ErrForbidden) {
			return e
		}
		if e != nil || role < Manager {
			if _, err := tx.ExecContext(ctx, "UPDATE shares SET revoked=1 WHERE id=?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

type Grant struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Role          int    `json:"role"`
}

func (s *Service) SetGrant(ctx context.Context, user, node string, g Grant) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := Require(ctx, tx, user, node, Manager); err != nil {
			return err
		}
		n, err := metadata.GetNode(ctx, tx, node)
		if err != nil {
			return err
		}
		if n.Kind != "folder" || g.Role < 0 || g.Role > Manager {
			return metadata.ErrInvalid
		}
		table := "users"
		if g.PrincipalType == "group" {
			table = "groups"
		} else if g.PrincipalType != "user" {
			return metadata.ErrInvalid
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=?", g.PrincipalID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return metadata.ErrNotFound
		}
		if g.Role == 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM grants WHERE node_id=? AND principal_type=? AND principal_id=?", node, g.PrincipalType, g.PrincipalID)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO grants VALUES(?,?,?,?) ON CONFLICT(node_id,principal_type,principal_id) DO UPDATE SET role=excluded.role", node, g.PrincipalType, g.PrincipalID, g.Role)
		}
		if err != nil {
			return err
		}
		return RevokeInvalid(ctx, tx)
	})
}
func (s *Service) Grants(ctx context.Context, user, node string) ([]Grant, error) {
	if err := Require(ctx, s.Store.DB, user, node, Manager); err != nil {
		return nil, err
	}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT principal_type,principal_id,role FROM grants WHERE node_id=? ORDER BY principal_type,principal_id", node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err = rows.Scan(&g.PrincipalType, &g.PrincipalID, &g.Role); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) CreateGroup(ctx context.Context, name string) (Group, error) {
	g := Group{security.Token(), name}
	if !metadata.ValidName(name) {
		return g, metadata.ErrInvalid
	}
	_, err := s.Store.DB.ExecContext(ctx, "INSERT INTO groups VALUES(?,?)", g.ID, g.Name)
	if err != nil {
		if metadata.IsUnique(err) {
			return g, metadata.ErrConflict
		}
		return g, err
	}
	return g, nil
}
func (s *Service) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT id,name FROM groups ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *Service) SetMember(ctx context.Context, group, user string, enabled bool) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM groups WHERE id=?", group).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return metadata.ErrNotFound
		}
		if enabled {
			if _, err := tx.ExecContext(ctx, "INSERT INTO memberships VALUES(?,?) ON CONFLICT DO NOTHING", group, user); err != nil {
				return metadata.ErrInvalid
			}
		} else {
			if _, err := tx.ExecContext(ctx, "DELETE FROM memberships WHERE group_id=? AND user_id=?", group, user); err != nil {
				return err
			}
		}
		return RevokeInvalid(ctx, tx)
	})
}
