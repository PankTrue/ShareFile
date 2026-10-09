package metadata

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Node struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	OwnerID   string `json:"owner_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	DeletedAt *int64 `json:"deleted_at,omitempty"`
	TrashRoot string `json:"trash_root,omitempty"`
}

const NodeColumns = "id,parent_id,owner_id,name,kind,size,created_at,deleted_at,trash_root"

func ScanNode(row interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var parent, trash sql.NullString
	err := row.Scan(&n.ID, &parent, &n.OwnerID, &n.Name, &n.Kind, &n.Size, &n.CreatedAt, &n.DeletedAt, &trash)
	n.ParentID = parent.String
	n.TrashRoot = trash.String
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return n, err
}
func GetNode(ctx context.Context, q Queries, id string) (Node, error) {
	return ScanNode(q.QueryRowContext(ctx, "SELECT "+NodeColumns+" FROM nodes WHERE id=?", id))
}

// GetSubtreeNode resolves only live nodes within root, including root itself.
// Callers must authorize access to root before using this boundary check.
func GetSubtreeNode(ctx context.Context, q Queries, root, id string) (Node, error) {
	if id == "" {
		id = root
	}
	n, err := GetNode(ctx, q, id)
	if err != nil {
		return n, err
	}
	if n.DeletedAt != nil {
		return n, ErrNotFound
	}
	var count int
	err = q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT id,parent_id FROM nodes WHERE id=? AND deleted_at IS NULL UNION ALL
 SELECT n.id,n.parent_id FROM nodes n JOIN ancestors a ON n.id=a.parent_id WHERE n.deleted_at IS NULL
 ) SELECT count(*) FROM ancestors WHERE id=?`, id, root).Scan(&count)
	if err == nil && count != 1 {
		err = ErrNotFound
	}
	return n, err
}
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 240 || !utf8.ValidString(name) || strings.TrimSpace(name) != name || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func UniqueName(ctx context.Context, q Queries, parent, name, exclude string) (string, error) {
	stem, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	candidate := name
	for i := 1; ; i++ {
		var count int
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE (parent_id=? OR (parent_id IS NULL AND ?='')) AND name=? AND deleted_at IS NULL AND id<>?", parent, parent, candidate, exclude).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		candidate = fmtName(stem, ext, i)
		if !ValidName(candidate) {
			return "", ErrConflict
		}
	}
}
func fmtName(stem, ext string, i int) string { return stem + " (" + strconv.Itoa(i) + ")" + ext }
