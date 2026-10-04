package shares

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

var ErrPassword = errors.New("share password required")

type Share struct {
	ID                string `json:"id"`
	NodeID            string `json:"node_id"`
	UserID            string `json:"user_id"`
	CreatedAt         int64  `json:"created_at"`
	ExpiresAt         *int64 `json:"expires_at,omitempty"`
	Revoked           bool   `json:"revoked"`
	PasswordProtected bool   `json:"password_protected"`
}
type Service struct {
	Store     *metadata.Store
	Files     *files.Service
	Passwords *security.Passwords
}

func (s *Service) Create(ctx context.Context, user, node, password string, expires *int64) (Share, string, error) {
	token := security.Token()
	sh := Share{ID: security.Token(), NodeID: node, UserID: user, CreatedAt: time.Now().Unix(), ExpiresAt: expires, PasswordProtected: password != ""}
	if expires != nil && *expires <= time.Now().Unix() {
		return sh, "", metadata.ErrInvalid
	}
	hash := ""
	if password != "" {
		var err error
		hash, err = s.Passwords.Hash(ctx, password)
		if err != nil {
			return sh, "", metadata.ErrInvalid
		}
	}
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if e := s.Files.Disk.Check(); e != nil {
			return e
		}
		if e := access.Require(ctx, tx, user, node, access.Manager); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO shares(id,node_id,user_id,hash,password,created_at,expires_at) VALUES(?,?,?,?,?,?,?)", sh.ID, node, user, security.Digest(token), hash, sh.CreatedAt, expires)
		return e
	})
	return sh, token, err
}
func scan(row interface{ Scan(...any) error }) (Share, string, error) {
	var sh Share
	var password string
	err := row.Scan(&sh.ID, &sh.NodeID, &sh.UserID, &password, &sh.CreatedAt, &sh.ExpiresAt, &sh.Revoked)
	sh.PasswordProtected = password != ""
	if errors.Is(err, sql.ErrNoRows) {
		err = metadata.ErrNotFound
	}
	return sh, password, err
}
func (s *Service) lookup(ctx context.Context, token string) (Share, string, error) {
	if len(token) != 43 {
		return Share{}, "", metadata.ErrNotFound
	}
	sh, password, err := scan(s.Store.DB.QueryRowContext(ctx, "SELECT id,node_id,user_id,password,created_at,expires_at,revoked FROM shares WHERE hash=?", security.Digest(token)))
	if err != nil {
		return sh, "", err
	}
	if sh.Revoked || (sh.ExpiresAt != nil && *sh.ExpiresAt <= time.Now().Unix()) {
		return sh, "", metadata.ErrNotFound
	}
	role, e := access.Role(ctx, s.Store.DB, sh.UserID, sh.NodeID)
	if e != nil && !errors.Is(e, metadata.ErrNotFound) && !errors.Is(e, metadata.ErrForbidden) {
		return sh, "", e
	}
	if e != nil || role < access.Manager {
		_, err = s.Store.DB.ExecContext(ctx, "UPDATE shares SET revoked=1 WHERE id=?", sh.ID)
		if err != nil {
			return sh, "", err
		}
		return sh, "", metadata.ErrNotFound
	}
	return sh, password, nil
}
func (s *Service) Authorize(ctx context.Context, token, session string) (Share, error) {
	sh, password, err := s.lookup(ctx, token)
	if err != nil {
		return sh, err
	}
	if password != "" {
		var count int
		if err = s.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM share_sessions WHERE hash=? AND share_id=? AND expires_at>?", security.Digest(session), sh.ID, time.Now().Unix()).Scan(&count); err != nil {
			return sh, err
		}
		if count != 1 {
			return sh, ErrPassword
		}
	}
	return sh, nil
}
func (s *Service) Unlock(ctx context.Context, token, password string) (string, error) {
	sh, encoded, err := s.lookup(ctx, token)
	if err != nil {
		return "", err
	}
	if encoded != "" && !s.Passwords.Verify(ctx, password, encoded) {
		return "", metadata.ErrForbidden
	}
	guest := security.Token()
	expires := time.Now().Add(24 * time.Hour).Unix()
	if sh.ExpiresAt != nil {
		expires = min(expires, *sh.ExpiresAt)
	}
	_, err = s.Store.DB.ExecContext(ctx, "INSERT INTO share_sessions VALUES(?,?,?)", security.Digest(guest), sh.ID, expires)
	return guest, err
}
func (s *Service) ResolveNode(ctx context.Context, sh Share, id string) (metadata.Node, error) {
	if err := s.Files.Disk.Check(); err != nil {
		return metadata.Node{}, err
	}
	if id == "" {
		id = sh.NodeID
	}
	n, err := metadata.GetNode(ctx, s.Store.DB, id)
	if err != nil {
		return n, err
	}
	if n.DeletedAt != nil {
		return n, metadata.ErrNotFound
	}
	var count int
	err = s.Store.DB.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT id,parent_id FROM nodes WHERE id=? AND deleted_at IS NULL UNION ALL
 SELECT n.id,n.parent_id FROM nodes n JOIN ancestors a ON n.id=a.parent_id WHERE n.deleted_at IS NULL
 ) SELECT count(*) FROM ancestors WHERE id=?`, id, sh.NodeID).Scan(&count)
	if err != nil {
		return n, err
	}
	if count != 1 {
		return n, metadata.ErrNotFound
	}
	return n, nil
}
func (s *Service) List(ctx context.Context, user, node string, limit, offset int) ([]Share, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, metadata.ErrInvalid
	}
	if node != "" {
		if err := access.Require(ctx, s.Store.DB, user, node, access.Manager); err != nil {
			return nil, err
		}
	}
	query := "SELECT id,node_id,user_id,password,created_at,expires_at,revoked FROM shares WHERE user_id=? ORDER BY created_at DESC,id LIMIT ? OFFSET ?"
	key := user
	if node != "" {
		query = "SELECT id,node_id,user_id,password,created_at,expires_at,revoked FROM shares WHERE node_id=? ORDER BY created_at DESC,id LIMIT ? OFFSET ?"
		key = node
	}
	rows, err := s.Store.DB.QueryContext(ctx, query, key, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, _, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}
func (s *Service) Revoke(ctx context.Context, user, id string) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		sh, _, err := scan(tx.QueryRowContext(ctx, "SELECT id,node_id,user_id,password,created_at,expires_at,revoked FROM shares WHERE id=?", id))
		if err != nil {
			return err
		}
		if sh.UserID != user {
			if err = access.Require(ctx, tx, user, sh.NodeID, access.Manager); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE shares SET revoked=1 WHERE id=?", id)
		return err
	})
}
