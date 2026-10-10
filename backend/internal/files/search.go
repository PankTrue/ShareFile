package files

import (
	"context"
	"database/sql"
	"path"
	"strings"
	"unicode/utf8"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
)

type SearchOptions struct {
	Query, Type, View           string
	CreatedAfter, CreatedBefore *int64
	MinSize, MaxSize            *int64
	Limit, Offset               int
}

type SearchFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SearchNode struct {
	metadata.Node
	EffectiveRole int            `json:"effective_role"`
	Favorite      bool           `json:"favorite"`
	Path          []SearchFolder `json:"path"`
}

type SearchResult struct {
	Items   []SearchNode `json:"items"`
	HasMore bool         `json:"has_more"`
}

func (o SearchOptions) valid() bool {
	if o.View != "" && o.View != "favorites" && o.View != "recent" {
		return false
	}
	if len(o.Query) > 240 || !utf8.ValidString(o.Query) || o.Limit < 1 || o.Limit > 100 || o.Offset < 0 {
		return false
	}
	switch o.Type {
	case "", "file", "folder", "image", "document", "audio", "video", "archive", "other":
	default:
		return false
	}
	for _, v := range []*int64{o.CreatedAfter, o.CreatedBefore, o.MinSize, o.MaxSize} {
		if v != nil && *v < 0 {
			return false
		}
	}
	return !(o.CreatedAfter != nil && o.CreatedBefore != nil && *o.CreatedAfter >= *o.CreatedBefore) &&
		!(o.MinSize != nil && o.MaxSize != nil && *o.MinSize > *o.MaxSize)
}

// File types describe filename extensions, not a claim about the file's contents.
func fileType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".heic", ".heif", ".avif", ".tif", ".tiff", ".ico":
		return "image"
	case ".txt", ".md", ".pdf", ".doc", ".docx", ".odt", ".rtf", ".xls", ".xlsx", ".ods", ".csv", ".tsv", ".ppt", ".pptx", ".odp", ".html", ".htm", ".xml", ".json", ".yaml", ".yml", ".log":
		return "document"
	case ".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".opus", ".wma", ".aiff":
		return "audio"
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".m4v", ".mpeg", ".mpg", ".wmv", ".3gp":
		return "video"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".tgz", ".zst":
		return "archive"
	default:
		return "other"
	}
}

// Search traverses only accessible live nodes. UNION bounds overlapping grants to
// at most three roles per node; filtering and pagination never see private nodes.
// A read snapshot keeps roles, results and safe breadcrumbs consistent.
func (s *Service) Search(ctx context.Context, user string, o SearchOptions) (SearchResult, error) {
	out := SearchResult{Items: []SearchNode{}}
	o.Query = strings.TrimSpace(o.Query)
	if !o.valid() {
		return out, metadata.ErrInvalid
	}
	if err := s.Disk.Check(); err != nil {
		return out, err
	}
	tx, err := s.Store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, "SELECT active=1 FROM users WHERE id=?", user).Scan(&active); err != nil || !active {
		if err == nil || err == sql.ErrNoRows {
			err = metadata.ErrForbidden
		}
		return out, err
	}
	query := `WITH RECURSIVE reachable(id,role) AS (
	 SELECT id,3 FROM nodes WHERE deleted_at IS NULL AND (
	  (parent_id IS NULL AND owner_id=?) OR EXISTS(SELECT 1 FROM users WHERE id=? AND admin=1)
	 )
	 UNION SELECT n.id,g.role FROM nodes n JOIN grants g ON g.node_id=n.id WHERE n.deleted_at IS NULL AND (
	  (g.principal_type='user' AND g.principal_id=?) OR
	  (g.principal_type='group' AND g.principal_id IN (SELECT group_id FROM memberships WHERE user_id=?))
	 )
	 UNION SELECT n.id,r.role FROM nodes n JOIN reachable r ON n.parent_id=r.id WHERE n.deleted_at IS NULL
	), allowed AS (SELECT id,max(role) AS role FROM reachable GROUP BY id)
	 SELECT n.id,n.parent_id,n.owner_id,n.name,n.kind,n.size,n.created_at,n.deleted_at,n.trash_root,a.role
	 ,EXISTS(SELECT 1 FROM favorites f WHERE f.user_id=? AND f.node_id=n.id)
	 FROM nodes n JOIN allowed a ON a.id=n.id WHERE n.deleted_at IS NULL`
	args := []any{user, user, user, user, user}
	if o.View == "favorites" {
		query += " AND EXISTS(SELECT 1 FROM favorites f WHERE f.user_id=? AND f.node_id=n.id)"
		args = append(args, user)
	} else if o.View == "recent" {
		query += " AND n.kind='file'"
	}
	if o.Type == "folder" {
		query += " AND n.kind='folder'"
	} else if o.Type != "" || o.MinSize != nil || o.MaxSize != nil {
		query += " AND n.kind='file'"
	}
	for _, filter := range []struct {
		clause string
		value  *int64
	}{{" AND n.created_at>=?", o.CreatedAfter}, {" AND n.created_at<?", o.CreatedBefore}, {" AND n.size>=?", o.MinSize}, {" AND n.size<=?", o.MaxSize}} {
		if filter.value != nil {
			query += filter.clause
			args = append(args, *filter.value)
		}
	}
	// A folder has no aggregate size: any size constraint excludes folders.
	if o.Type == "folder" && (o.MinSize != nil || o.MaxSize != nil) {
		query += " AND n.kind='file'"
	}
	if o.View == "recent" {
		query += " ORDER BY n.created_at DESC,n.id"
	} else {
		query += " ORDER BY n.name,n.id"
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	needle := strings.ToLower(o.Query)
	skipped := 0
	for rows.Next() {
		var n SearchNode
		var parent, trash sql.NullString
		err = rows.Scan(&n.ID, &parent, &n.OwnerID, &n.Name, &n.Kind, &n.Size, &n.CreatedAt, &n.DeletedAt, &trash, &n.EffectiveRole, &n.Favorite)
		if err != nil {
			break
		}
		n.ParentID, n.TrashRoot = parent.String, trash.String
		// Go's Unicode folding supports Cyrillic too; SQLite lower() is ASCII-only.
		if !strings.Contains(strings.ToLower(n.Name), needle) || (o.Type != "" && o.Type != "file" && o.Type != "folder" && fileType(n.Name) != o.Type) {
			continue
		}
		if skipped < o.Offset {
			skipped++
			continue
		}
		if len(out.Items) == o.Limit {
			out.HasMore = true
			break
		}
		out.Items = append(out.Items, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close() // The store has one connection; release the cursor before breadcrumbs.
	if err != nil {
		return out, err
	}
	paths := map[string][]SearchFolder{"": {}}
	var folderPath func(string) ([]SearchFolder, error)
	folderPath = func(id string) ([]SearchFolder, error) {
		if cached, ok := paths[id]; ok {
			return cached, nil
		}
		role, err := access.Role(ctx, tx, user, id)
		if err != nil {
			return nil, err
		}
		if role < access.Reader {
			paths[id] = []SearchFolder{}
			return paths[id], nil
		}
		n, err := metadata.GetNode(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		parent, err := folderPath(n.ParentID)
		if err != nil {
			return nil, err
		}
		paths[id] = append(append([]SearchFolder{}, parent...), SearchFolder{n.ID, n.Name})
		return paths[id], nil
	}
	for i := range out.Items {
		out.Items[i].Path, err = folderPath(out.Items[i].ParentID)
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
