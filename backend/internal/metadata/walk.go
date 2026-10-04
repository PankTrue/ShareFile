package metadata

import "context"

// EachID walks a stable ID-ordered result without holding a cursor while visit
// queries or changes the same database. The query must end in an ID cursor
// placeholder followed by ORDER BY ID LIMIT 128; args excludes that cursor.
func EachID(ctx context.Context, q Queries, query string, args []any, visit func(string) error) error {
	cursor := ""
	for {
		params := append(append([]any(nil), args...), cursor)
		rows, err := q.QueryContext(ctx, query, params...)
		if err != nil {
			return err
		}
		ids := make([]string, 0, 128)
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		cursor = ids[len(ids)-1]
		for _, id := range ids {
			if err = visit(id); err != nil {
				return err
			}
		}
	}
}
