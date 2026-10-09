package httpapi

import (
	"archive/zip"
	"compress/flate"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"time"

	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

type archiveResolver func(string) (metadata.Node, error)

func (a *App) downloadArchive(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	n, err := a.Files.Node(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.serveArchive(w, r, n, func(id string) (metadata.Node, error) {
		if _, err := a.Files.Node(r.Context(), u.ID, n.ID); err != nil {
			return metadata.Node{}, err
		}
		return metadata.GetSubtreeNode(r.Context(), a.Store.DB, n.ID, id)
	})
}

func (a *App) guestArchive(w http.ResponseWriter, r *http.Request) {
	_, n, ok := a.guestNode(w, r)
	if !ok {
		return
	}
	session := ""
	if c, err := r.Cookie(a.guestCookie(r.PathValue("token"))); err == nil {
		session = c.Value
	}
	a.serveArchive(w, r, n, func(id string) (metadata.Node, error) {
		sh, err := a.Shares.Authorize(r.Context(), r.PathValue("token"), session)
		if err != nil {
			return metadata.Node{}, err
		}
		if _, err := a.Shares.ResolveNode(r.Context(), sh, n.ID); err != nil {
			return metadata.Node{}, err
		}
		return metadata.GetSubtreeNode(r.Context(), a.Store.DB, n.ID, id)
	})
}

func (a *App) serveArchive(w http.ResponseWriter, r *http.Request, root metadata.Node, resolve archiveResolver) {
	if root.Kind != "folder" || !metadata.ValidName(root.Name) {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": root.Name + ".zip"}))
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	// Length is unknown until completion. HEAD must not read files or build a ZIP.
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	out := &archiveOutput{ctx: r.Context(), idleWriter: idleWriter{ResponseWriter: w}}
	z := zip.NewWriter(out)
	// Keep router CPU costs low, even for large text collections.
	compressor, err := flate.NewWriter(io.Discard, flate.BestSpeed)
	if err != nil {
		fail(w, r, err)
		return
	}
	z.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		// ZIP writes entries sequentially; reuse the same compression buffers.
		compressor.Reset(w)
		return compressor, nil
	})
	err = a.writeArchive(r.Context(), z, root, resolve, func() error {
		if err := z.Flush(); err != nil {
			return err
		}
		err := http.NewResponseController(w).Flush()
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	})
	if err == nil {
		err = z.Close()
	}
	if err != nil {
		// A failed stream must not be finalized as a valid but incomplete archive,
		// or have a JSON error appended to ZIP bytes already sent to the client.
		if !out.started {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Accept-Ranges")
			fail(w, r, err)
			return
		}
		slog.Warn("archive transfer interrupted", "route", r.Pattern, "error", err)
		panic(http.ErrAbortHandler)
	}
}

type archiveOutput struct {
	ctx context.Context
	idleWriter
	started bool
}

func (w *archiveOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.started = true
	return w.idleWriter.Write(p)
}

type archiveInput struct {
	ctx context.Context
	io.Reader
}

func (r *archiveInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func archiveEntry(z *zip.Writer, n metadata.Node, name string) (io.Writer, error) {
	if !metadata.ValidName(n.Name) || len(name) > 65534 {
		return nil, metadata.ErrInvalid
	}
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Unix(n.CreatedAt, 0)}
	h.SetMode(0644)
	if n.Kind == "folder" {
		h.Name += "/"
		h.Method = zip.Store
		h.SetMode(os.ModeDir | 0755)
	}
	return z.CreateHeader(h)
}

func (a *App) writeArchive(ctx context.Context, z *zip.Writer, root metadata.Node, resolve archiveResolver, flush func() error) error {
	if _, err := resolve(root.ID); err != nil {
		return err
	}
	if _, err := archiveEntry(z, root, root.Name); err != nil {
		return err
	}
	// Send the first ZIP bytes before traversing or reading the tree.
	if err := flush(); err != nil {
		return err
	}
	type frame struct {
		node                   metadata.Node
		path, lastName, lastID string
	}
	stack := []frame{{node: root, path: root.Name}}
	buffer := make([]byte, 32*1024)
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := &stack[len(stack)-1]
		liveFolder, err := resolve(current.node.ID)
		if err != nil {
			return err
		}
		if liveFolder.ParentID != current.node.ParentID || liveFolder.Name != current.node.Name {
			return metadata.ErrConflict
		}
		// Keyset traversal keeps no open SQL rows during a slow download and
		// does not load the whole tree or depend on the listing's pagination.
		n, err := metadata.ScanNode(a.Store.DB.QueryRowContext(ctx, "SELECT "+metadata.NodeColumns+` FROM nodes
 WHERE parent_id=? AND deleted_at IS NULL AND (name,id)>(?,?)
 ORDER BY name,id LIMIT 1`, current.node.ID, current.lastName, current.lastID))
		if errors.Is(err, metadata.ErrNotFound) {
			stack = stack[:len(stack)-1]
			continue
		}
		if err != nil {
			return err
		}
		current.lastName, current.lastID = n.Name, n.ID
		// Recheck authorization and ancestry after enumeration: files may move,
		// permissions or a public link may change while earlier bytes stream.
		live, err := resolve(n.ID)
		if err != nil {
			return err
		}
		if live.ParentID != n.ParentID || live.Name != n.Name {
			return metadata.ErrConflict
		}
		name := current.path + "/" + n.Name
		if n.Kind == "folder" {
			if _, err := archiveEntry(z, n, name); err != nil {
				return err
			}
			stack = append(stack, frame{node: n, path: name})
			continue
		}
		if err := a.archiveFile(ctx, z, n, name, buffer); err != nil {
			return err
		}
	}
	// Catch revocation/expiry even if it happened during the final file.
	_, err := resolve(root.ID)
	return err
}

func (a *App) archiveFile(ctx context.Context, z *zip.Writer, n metadata.Node, name string, buffer []byte) error {
	f, err := a.Files.Disk.Root.Open("objects/" + n.ID)
	if err != nil {
		return metadata.ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != n.Size {
		return metadata.ErrUnavailable
	}
	out, err := archiveEntry(z, n, name)
	if err != nil {
		return err
	}
	size, err := io.CopyBuffer(out, &archiveInput{ctx: ctx, Reader: f}, buffer)
	if err == nil && size != n.Size {
		err = io.ErrUnexpectedEOF
	}
	return err
}
