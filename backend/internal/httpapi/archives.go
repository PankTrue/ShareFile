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
	"path"
	"strconv"
	"strings"
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
	a.serveZIP(w, r, root.Name+".zip", func(z *zip.Writer, flush func() error) error {
		return a.writeArchive(r.Context(), z, root, resolve, flush)
	})
}

func (a *App) downloadSelection(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	nodes, err := a.Files.Selection(r.Context(), u.ID, r.URL.Query()["id"])
	if err != nil {
		fail(w, r, err)
		return
	}
	// Search results can contain identical basenames from different folders.
	names := make([]string, len(nodes))
	used := map[string]bool{}
	for i, n := range nodes {
		name := n.Name
		for suffix := 1; used[name]; suffix++ {
			ext := path.Ext(n.Name)
			name = strings.TrimSuffix(n.Name, ext) + " (" + strconv.Itoa(suffix) + ")" + ext
		}
		if !metadata.ValidName(name) {
			fail(w, r, metadata.ErrConflict)
			return
		}
		names[i], used[name] = name, true
	}
	a.serveZIP(w, r, "Выбранные файлы.zip", func(z *zip.Writer, flush func() error) error {
		buffer := make([]byte, 32*1024)
		for i, root := range nodes {
			resolve := func(id string) (metadata.Node, error) {
				live, err := a.Files.Node(r.Context(), u.ID, root.ID)
				if err != nil {
					return metadata.Node{}, err
				}
				if live.ParentID != root.ParentID || live.Name != root.Name {
					return metadata.Node{}, metadata.ErrConflict
				}
				return metadata.GetSubtreeNode(r.Context(), a.Store.DB, root.ID, id)
			}
			if _, err := resolve(root.ID); err != nil {
				return err
			}
			if root.Kind == "folder" {
				if err := a.writeArchiveAt(r.Context(), z, root, names[i], resolve, flush); err != nil {
					return err
				}
			} else {
				if err := a.archiveFile(r.Context(), z, root, names[i], buffer); err != nil {
					return err
				}
				if _, err := resolve(root.ID); err != nil {
					return err
				}
				if err := flush(); err != nil {
					return err
				}
			}
		}
		// Recheck even the first entry after the final file has streamed.
		for _, root := range nodes {
			live, err := a.Files.Node(r.Context(), u.ID, root.ID)
			if err != nil {
				return err
			}
			if live.ParentID != root.ParentID || live.Name != root.Name {
				return metadata.ErrConflict
			}
		}
		return nil
	})
}

func (a *App) serveZIP(w http.ResponseWriter, r *http.Request, filename string, write func(*zip.Writer, func() error) error) {
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
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
	err = write(z, func() error {
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
	return a.writeArchiveAt(ctx, z, root, root.Name, resolve, flush)
}

func (a *App) writeArchiveAt(ctx context.Context, z *zip.Writer, root metadata.Node, name string, resolve archiveResolver, flush func() error) error {
	if _, err := resolve(root.ID); err != nil {
		return err
	}
	if _, err := archiveEntry(z, root, name); err != nil {
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
	stack := []frame{{node: root, path: name}}
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
