package httpapi

import (
	"bytes"
	_ "embed"
	"html/template"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/shares"
)

const textPreviewBytes = 256 * 1024

//go:embed templates/share.html
var shareHTML string

//go:embed templates/share.css
var shareCSS string

var sharePageTemplate = template.Must(template.New("share").Funcs(template.FuncMap{
	"size": shareSize,
	"t":    shareText,
}).Parse(shareHTML))

type shareCrumb struct {
	ID, Name, URL string
}

type sharePageData struct {
	Language, EnglishURL, RussianURL              string
	Title, Base, BackURL, DownloadURL, ContentURL string
	Node                                          metadata.Node
	Trail                                         []shareCrumb
	Nodes                                         []metadata.Node
	Locked, Previous, Next                        bool
	PreviousOffset, NextOffset, Limit             int
	Preview                                       filePreview
}

type filePreview struct {
	Kind, MediaType, Text, Message string
	Truncated                      bool
}

func shareStyles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = io.WriteString(w, shareCSS)
}

func renderShare(w http.ResponseWriter, r *http.Request, status int, data sharePageData) {
	data.Language = preferredLanguage(r)
	data.EnglishURL, data.RussianURL = languageURL(r, "en"), languageURL(r, "ru")
	if value := r.URL.Query().Get("lang"); value == "en" || value == "ru" {
		http.SetCookie(w, &http.Cookie{Name: "sf_language", Value: value, Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	}
	if data.Locked {
		data.Title = shareText(data.Language, data.Title)
	}
	w.Header().Set("Content-Language", data.Language)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if data.Locked {
		// no-referrer makes browser form POSTs use Origin: null, failing checkOrigin.
		// Keep the same-origin form usable without sending the share URL elsewhere.
		w.Header().Set("Referrer-Policy", "same-origin")
	}
	w.WriteHeader(status)
	_ = sharePageTemplate.Execute(w, data)
}

func (a *App) sharePage(r *http.Request, sh shares.Share, n metadata.Node) (sharePageData, error) {
	data := sharePageData{Title: n.Name, Base: "/s/" + r.PathValue("token"), Node: n}
	// Stop at the shared root: names and links above it must never reach the guest.
	rows, err := a.Store.DB.QueryContext(r.Context(), `WITH RECURSIVE trail(id,parent_id,name,depth) AS (
 SELECT id,parent_id,name,0 FROM nodes WHERE id=? AND deleted_at IS NULL
 UNION ALL SELECT n.id,n.parent_id,n.name,t.depth+1 FROM nodes n JOIN trail t ON n.id=t.parent_id
 WHERE t.id<>? AND n.deleted_at IS NULL
 ) SELECT id,name FROM trail ORDER BY depth DESC`, n.ID, sh.NodeID)
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var crumb shareCrumb
		if err := rows.Scan(&crumb.ID, &crumb.Name); err != nil {
			return data, err
		}
		crumb.URL = data.Base
		if crumb.ID != sh.NodeID {
			crumb.URL += "/nodes/" + crumb.ID
		}
		data.Trail = append(data.Trail, crumb)
	}
	if err := rows.Err(); err != nil {
		return data, err
	}
	if len(data.Trail) == 0 || data.Trail[0].ID != sh.NodeID {
		return data, metadata.ErrNotFound
	}
	if len(data.Trail) > 1 {
		data.BackURL = data.Trail[len(data.Trail)-2].URL
	}
	data.DownloadURL = data.Base + "/nodes/" + n.ID
	data.ContentURL = data.DownloadURL + "/preview"
	if n.Kind == "folder" {
		data.DownloadURL += "/archive"
	}
	return data, nil
}

// Content sniffing, rather than the filename, decides which images can be served inline.
// SVG/HTML/XML are only ever shown as escaped text, never as active documents.
func previewType(name string, sample []byte) (kind, media string) {
	media = http.DetectContentType(sample)
	switch media {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp":
		return "image", media
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".markdown", ".log", ".csv", ".tsv", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".xml", ".html", ".htm", ".svg", ".css", ".js", ".jsx", ".ts", ".tsx", ".go", ".py", ".rb", ".sh", ".sql", ".rs", ".java", ".c", ".h", ".cpp":
		return "text", "text/plain; charset=utf-8"
	}
	if strings.HasPrefix(media, "text/plain") {
		return "text", "text/plain; charset=utf-8"
	}
	return "", ""
}

func previewSample(f *os.File) ([]byte, error) {
	sample := make([]byte, 512)
	n, err := io.ReadFull(f, sample)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	_, err = f.Seek(0, io.SeekStart)
	return sample[:n], err
}

func readPreview(f *os.File, name string) (filePreview, error) {
	preview := filePreview{Message: "Предпросмотр этого формата пока недоступен. Скачайте файл, чтобы открыть его."}
	sample, err := previewSample(f)
	if err != nil {
		return preview, err
	}
	preview.Kind, preview.MediaType = previewType(name, sample)
	if preview.Kind != "text" {
		return preview, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, textPreviewBytes+1))
	if err != nil {
		return preview, err
	}
	preview.Truncated = len(data) > textPreviewBytes
	if preview.Truncated {
		data = data[:textPreviewBytes]
		// The boundary may split a UTF-8 rune; discard only that incomplete suffix.
		for i := len(data) - 1; i >= max(0, len(data)-utf8.UTFMax); i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRune(data[i:]) {
					data = data[:i]
				}
				break
			}
		}
	}
	valid := utf8.Valid(data)
	for _, b := range data {
		if b < 32 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			valid = false
			break
		}
	}
	if !valid {
		preview.Kind = ""
		preview.Truncated = false
		preview.Message = "Файл содержит двоичные данные или текст в кодировке, отличной от UTF-8. Скачайте его, чтобы открыть."
		return preview, nil
	}
	preview.Text = string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	return preview, nil
}

func (a *App) guestView(w http.ResponseWriter, r *http.Request) {
	sh, n, ok := a.guestNode(w, r)
	if !ok {
		return
	}
	data, err := a.sharePage(r, sh, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	if n.Kind != "file" {
		http.Redirect(w, r, data.Trail[len(data.Trail)-1].URL, http.StatusSeeOther)
		return
	}
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	f, err := a.Files.Disk.Root.Open("objects/" + n.ID)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	defer f.Close()
	data.Preview, err = readPreview(f, n.Name)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	renderShare(w, r, http.StatusOK, data)
}

func (a *App) guestPreview(w http.ResponseWriter, r *http.Request) {
	_, n, ok := a.guestNode(w, r)
	if !ok {
		return
	}
	if n.Kind != "file" {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	f, err := a.Files.Disk.Root.Open("objects/" + n.ID)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	defer f.Close()
	sample, err := previewSample(f)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	kind, media := previewType(n.Name, sample)
	if kind != "image" {
		// All other formats use the escaped HTML view or the attachment endpoint.
		fail(w, r, metadata.ErrInvalid)
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": n.Name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("ETag", strconv.Quote(n.ID))
	http.ServeContent(&idleWriter{ResponseWriter: w}, r, n.Name, time.Unix(n.CreatedAt, 0), f)
}
