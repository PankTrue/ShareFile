package httpapi

import (
	"bytes"
	"encoding/binary"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

type nodePreview struct {
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Text      string `json:"text"`
	Message   string `json:"message"`
	Truncated bool   `json:"truncated"`
}

// Only recognized passive formats reach the inline endpoint. Extensions may
// distinguish audio from video containers, but never authorize active content.
func browserMediaType(name string, sample []byte) (kind, media string) {
	media = http.DetectContentType(sample)
	ext := strings.ToLower(path.Ext(name))
	switch media {
	case "application/pdf":
		return "pdf", media
	case "audio/mpeg", "audio/wave":
		return "audio", media
	case "application/ogg":
		if ext == ".ogv" {
			return "video", "video/ogg"
		}
		return "audio", "audio/ogg"
	case "video/mp4":
		if ext == ".m4a" || ext == ".m4b" {
			return "audio", "audio/mp4"
		}
		return "video", media
	case "video/webm":
		if ext == ".weba" {
			return "audio", "audio/webm"
		}
		return "video", media
	}
	if bytes.HasPrefix(sample, []byte("fLaC")) {
		return "audio", "audio/flac"
	}
	// M4A and some MP4 writers use ISO/M4A brands instead of the mp4 brand
	// recognized by net/http. Require an actual, bounded ftyp box.
	if len(sample) >= 16 && bytes.Equal(sample[4:8], []byte("ftyp")) {
		size := int(binary.BigEndian.Uint32(sample[:4]))
		if size >= 16 && size <= len(sample) && size%4 == 0 {
			for i := 8; i < size; i += 4 {
				if i == 12 {
					continue
				}
				switch string(sample[i : i+4]) {
				case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "avc1", "M4A ", "M4B ":
					if ext == ".m4a" || ext == ".m4b" {
						return "audio", "audio/mp4"
					}
					return "video", "video/mp4"
				}
			}
		}
	}
	if len(sample) >= 4 && sample[0] == 0xff {
		if sample[1]&0xf6 == 0xf0 { // ADTS AAC, either MPEG version, layer zero.
			return "audio", "audio/aac"
		}
		if sample[1]&0xe0 == 0xe0 && sample[1]&0x18 != 0x08 && sample[1]&0x06 == 0x02 && sample[2]&0xf0 != 0xf0 && sample[2]&0xf0 != 0 && sample[2]&0x0c != 0x0c {
			return "audio", "audio/mpeg"
		}
	}
	return "", ""
}

func (a *App) preview(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	n, f, err := a.Files.Open(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer f.Close()
	sample, err := previewSample(f)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	kind, media := browserMediaType(n.Name, sample)
	if kind != "" {
		jsonReply(w, http.StatusOK, nodePreview{Kind: kind, MediaType: media})
		return
	}
	p, err := readPreview(f, n.Name)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	if p.Kind == "" {
		p.Kind = "unsupported"
	} else {
		p.Message = ""
	}
	jsonReply(w, http.StatusOK, nodePreview{Kind: p.Kind, MediaType: p.MediaType, Text: p.Text, Message: p.Message, Truncated: p.Truncated})
}

func (a *App) previewContent(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	n, f, err := a.Files.Open(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer f.Close()
	sample, err := previewSample(f)
	if err != nil {
		fail(w, r, metadata.ErrUnavailable)
		return
	}
	kind, media := browserMediaType(n.Name, sample)
	if kind == "" {
		kind, media = previewType(n.Name, sample)
	}
	if kind != "image" && kind != "pdf" && kind != "audio" && kind != "video" {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": n.Name}))
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	// The browser's isolated native PDF viewer needs plugins enabled. Other
	// passive content also receives sandbox; HTML/SVG/XML never reach this route.
	csp := "default-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"
	if kind != "pdf" {
		csp += "; sandbox"
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("ETag", strconv.Quote(n.ID))
	http.ServeContent(&idleWriter{ResponseWriter: w}, r, n.Name, time.Unix(n.CreatedAt, 0), f)
}
