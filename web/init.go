package web

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var localLoc *time.Location

func init() {
	localLoc, _ = time.LoadLocation("Local")
	gin.SetMode(gin.ReleaseMode)
	// web engine
	g := gin.Default()
	// load html files, tpl files
	g.LoadHTMLGlob("templates/*")
	// load resource files, js/css etc.
	g.Static("/content", "content")
	// set admin router
	setAdminRouter(g.Group("/admin"))
	// set user router
	setUserRouter(g.Group("/"))
	// set payment router
	setPaymentRouter(g.Group("/payment"))
	// set server info
	s := &http.Server{
		Addr:           ":8080",
		Handler:        g,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	// start and listen port
	s.ListenAndServe()
}
