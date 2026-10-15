package nasui

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNASAssetsAndBasePath(t *testing.T) {
	r := gin.New()
	Register(r.Group("/alist"))
	for _, name := range []string{"/@nas", "/bitqiu-assets/app.js", "/bitqiu-assets/nav.js", "/bitqiu-assets/style.css"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/alist"+name, nil))
		if w.Code != 200 || w.Body.Len() == 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("asset %s: %d", name, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/alist/bitqiu-assets/secret", nil))
	if w.Code != 404 {
		t.Fatal("exposed unexpected asset")
	}
	if got := NavigationScript("/alist/"); !strings.Contains(got, `src="/alist/bitqiu-assets/nav.js"`) {
		t.Fatal(got)
	}
	if !strings.Contains(NavigationScript(""), "<script async ") {
		t.Fatal("navigation script must not block parsing the official frontend's root element")
	}
	if strings.Contains(NavigationScript(`/"><img src=x>`), `<img`) {
		t.Fatal("unescaped script path")
	}
}
