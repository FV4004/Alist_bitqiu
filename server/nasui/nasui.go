package nasui

import (
	"embed"
	"html"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var assets embed.FS

func NavigationScript(base string) string {
	return `<script src="` + html.EscapeString(strings.TrimSuffix(base, "/")+"/bitqiu-assets/nav.js") + `"></script>`
}
func Register(g *gin.RouterGroup) {
	g.GET("/@nas", func(c *gin.Context) {
		b, _ := assets.ReadFile("web/index.html")
		c.Header("Cache-Control", "no-store")
		c.Data(200, "text/html; charset=utf-8", b)
	})
	g.GET("/bitqiu-assets/:asset", func(c *gin.Context) {
		name := c.Param("asset")
		kind := "application/javascript; charset=utf-8"
		if name == "style.css" {
			kind = "text/css; charset=utf-8"
		} else if name != "app.js" && name != "nav.js" {
			c.Status(404)
			return
		}
		b, err := assets.ReadFile("web/" + name)
		if err != nil {
			c.Status(404)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(200, kind, b)
	})
}
