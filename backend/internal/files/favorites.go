package files

import (
	"context"
	"database/sql"

	"sharefile/backend/internal/access"
)

func (s *Service) IsFavorite(ctx context.Context, user, node string) (bool, error) {
	var favorite bool
	err := s.Store.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM favorites WHERE user_id=? AND node_id=?)", user, node).Scan(&favorite)
	return favorite, err
}

// Favorites are personal bookmarks. Adding requires current read access; removing
// remains possible after deletion or revocation and never reveals node metadata.
// Explicit PUT/DELETE semantics make retries safe after a lost response.
func (s *Service) SetFavorite(ctx context.Context, user, node string, favorite bool) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := s.Disk.Check(); err != nil {
			return err
		}
		if !favorite {
			_, err := tx.ExecContext(ctx, "DELETE FROM favorites WHERE user_id=? AND node_id=?", user, node)
			return err
		}
		if err := access.Require(ctx, tx, user, node, access.Reader); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO favorites(user_id,node_id) VALUES(?,?) ON CONFLICT DO NOTHING", user, node)
		return err
	})
}
