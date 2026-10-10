package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedPageLanguagePreference(t *testing.T) {
	for _, test := range []struct{ query, cookie, lang, heading string }{
		{"", "", "en", "Link password"},
		{"?lang=ru", "", "ru", "Пароль ссылки"},
		{"", "sf_language=ru", "ru", "Пароль ссылки"},
		{"?lang=en", "sf_language=ru", "en", "Link password"},
		{"?lang=unknown", "sf_language=unknown", "en", "Link password"},
	} {
		t.Run(test.query+test.cookie, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/s/test"+test.query, nil)
			r.Header.Set("Cookie", test.cookie)
			w := httptest.NewRecorder()
			renderShare(w, r, http.StatusUnauthorized, sharePageData{Title: "Пароль ссылки", Base: "/s/test", Locked: true})
			if w.Code != 401 || w.Header().Get("Content-Language") != test.lang || !strings.Contains(w.Body.String(), `<html lang="`+test.lang+`">`) || !strings.Contains(w.Body.String(), "<h1>"+test.heading+"</h1>") {
				t.Fatalf("incorrect localized page: %s", w.Body.String())
			}
			if w.Header().Get("Referrer-Policy") != "same-origin" {
				t.Fatal("password form policy changed")
			}
			if test.query == "?lang=ru" || test.query == "?lang=en" {
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Value != test.lang || cookies[0].Path != "/" || cookies[0].SameSite != http.SameSiteLaxMode {
					t.Fatal("language choice was not saved")
				}
			}
		})
	}
}

func TestSharedLanguageKeepsContentAndPagination(t *testing.T) {
	r := httptest.NewRequest("GET", "/s/test/nodes/folder?limit=10&offset=20", nil)
	if got := languageURL(r, "ru"); got != "/s/test/nodes/folder?lang=ru&limit=10&offset=20" {
		t.Fatalf("pagination lost: %s", got)
	}
	w := httptest.NewRecorder()
	renderShare(w, r, 200, sharePageData{Title: "Папка", Preview: filePreview{Kind: "text", Text: "Привет <script>alert(1)</script>"}})
	if !strings.Contains(w.Body.String(), "<h1>Папка</h1>") || !strings.Contains(w.Body.String(), "Привет &lt;script&gt;") {
		t.Fatal("user content was translated or not escaped")
	}
	if shareSize("en", 1024) != "1.0 KiB" || shareSize("ru", 1024) != "1.0 КиБ" {
		t.Fatal("sizes were not localized")
	}
}
