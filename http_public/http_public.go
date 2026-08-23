// Package http_public serves the cache's public HTTP API.
//
// The routing table is the whole contract a non-Go backend has to reproduce —
// see README.md. /install and / are this implementation's own pages; another
// backend reimplements /v1/* and nothing more.
//
//	GET    /                   status page
//	GET    /install            setup page, once installed only says so
//	POST   /install            write the config and show the key once
//	GET    /v1/info            server limits (no auth)
//	POST   /v1/chunks          store a chunk (auth, unless open_upload)
//	GET    /v1/chunks/{id}     fetch a chunk, Range-capable (no auth)
//	HEAD   /v1/chunks/{id}     as GET, headers only (no auth)
//	DELETE /v1/chunks/{id}     drop a chunk (auth, owner only)
//	GET    /swagger/*any       the interactive API playground
package http_public

import (
	"context"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/log"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"

	// Registers the generated OpenAPI spec served at /swagger.
	_ "github.com/ModderMule/emule-http-cache-go/docs_api"
)

// state is everything a request handler reads that can change while the server
// is running.
//
// It is swapped wholesale rather than mutated, so a request either sees the
// pre-install server or the post-install one and never a half-built mixture.
// The alternative — telling the operator to restart after using the install
// page — would be a worse first five minutes than the PHP server offers, since
// that one re-reads its config on every request.
type state struct {
	cfg       *config.Config
	store     *storage.Store
	quota     *storage.Quota
	installed bool
}

// Server holds the dependencies every handler needs.
type Server struct {
	current   atomic.Pointer[state]
	installer *install.Installer
	pages     *pageSet
	logger    log.Logger
	startedAt time.Time
	accessLog bool

	// reload rebuilds the server's state after the install page writes a
	// config. Supplied by the caller, which also owns the sweeper that has to
	// be restarted alongside it.
	reload func() (*config.Config, *storage.Store, *storage.Quota, error)

	// listen and staticFilePath are read before any state exists, so they are
	// captured at construction rather than read back off the config.
	listen   string
	timeouts config.ServerConfig
}

// Deps is everything a Server needs, gathered so the constructor does not grow
// a positional argument list nobody can read.
type Deps struct {
	Config    *config.Config
	Store     *storage.Store
	Quota     *storage.Quota
	GC        *storage.Gc
	Installer *install.Installer
	Logger    log.Logger

	// Reload rebuilds the config, store and quota after the install page writes
	// a config file. Optional: without it, an install needs a restart.
	Reload func() (*config.Config, *storage.Store, *storage.Quota, error)

	// AccessLog enables the per-request log line.
	AccessLog bool

	// Installed is false until a config file exists, which puts every /v1 route
	// behind a 503 and leaves /install as the whole user interface.
	Installed bool
}

// New builds a server over an already-loaded config.
func New(d Deps) (*Server, error) {
	pages, err := loadPages(d.Config.Server.StaticFilePath)
	if err != nil {
		return nil, err
	}

	s := &Server{
		installer: d.Installer,
		pages:     pages,
		logger:    d.Logger.WithFields(log.Fields{"module": "http_public"}),
		startedAt: time.Now(),
		accessLog: d.AccessLog,
		reload:    d.Reload,
		listen:    d.Config.Server.Addr,
		timeouts:  d.Config.Server,
	}
	s.current.Store(&state{
		cfg:       d.Config,
		store:     d.Store,
		quota:     d.Quota,
		installed: d.Installed,
	})

	// Say which pages came off disk: an operator who mistyped the directory
	// gets the embedded set silently, and this line is how they notice.
	if len(pages.overridden) > 0 {
		s.logger.Infof("page templates from %s: %s", d.Config.Server.StaticFilePath,
			strings.Join(pages.overridden, ", "))
	}

	return s, nil
}

// now returns the state this request should read.
func (s *Server) now() *state {
	return s.current.Load()
}

// Handler builds the gin engine. Exported so tests can drive it through
// httptest without opening a listener.
func (s *Server) Handler() http.Handler {
	gin.SetMode(s.timeouts.Mode)

	// Route gin's internal output through the structured logger via the
	// io.Writer adapter.
	gin.DefaultWriter = log.GetWriter()
	gin.DefaultErrorWriter = log.GetWriter()

	r := gin.New()

	// Both default to true and answer a near-miss path with a 301. eMuleQt
	// treats any 3xx on the chunk path as a failed fetch with no retry, so a
	// stray trailing slash would look like a dead cache rather than a 404.
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false

	// Off by default, which would fold a wrong method into the 404 handler and
	// lose the Allow header the contract specifies.
	r.HandleMethodNotAllowed = true

	r.Use(gin.RecoveryWithWriter(log.GetWriter()), s.requestLogger())

	r.NoRoute(func(c *gin.Context) { writeNotFound(c) })
	r.NoMethod(func(c *gin.Context) {
		writeMethodNotAllowed(c, c.Writer.Header().Get("Allow"))
	})

	s.registerRoutes(r)

	return r
}

// StartServer runs the HTTP server until ctx is cancelled, then shuts it down
// gracefully. It blocks until shutdown completes.
func (s *Server) StartServer(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.listen,
		Handler: s.Handler(),

		// ReadTimeout and WriteTimeout are deliberately left at the configured
		// zero: both are absolute deadlines from the start of a request, and a
		// 9.7 MB transfer to a slow peer would trip any value small enough to
		// be a useful stall detector. The download and ingest paths roll
		// per-slice deadlines instead.
		ReadTimeout:       s.timeouts.ReadTimeout,
		WriteTimeout:      s.timeouts.WriteTimeout,
		ReadHeaderTimeout: s.timeouts.ReadHeaderTimeout,
		IdleTimeout:       s.timeouts.IdleTimeout,

		// http.Server's error log routes through the structured logger too.
		ErrorLog: stdlog.New(log.GetWriter(), "", 0),
	}

	// Shut the server down when the application context is cancelled.
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		s.logger.Infof("shutting down...")

		// Shutdown does not interrupt a handler that is already running, so the
		// window has to outlast an upload in flight: 9,728,016 bytes at a real
		// 100 KB/s is 97 seconds, and a conventional 10-second grace would
		// guillotine it.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.timeouts.ShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Errorf("graceful shutdown failed: %v", err)
			_ = srv.Close()
		}
		close(done)
	}()

	if st := s.now(); st.installed {
		s.logger.Infof("emule-http-cache listening on %s, storage=%s", s.listen, st.cfg.Storage.DataDir)
	} else {
		s.logger.Warnf("not installed yet — every /v1 route answers 503 until a config file exists")
	}
	s.logger.Infof("Server ready — open %s", serverURL(s.listen))
	s.logger.Infof("API docs — open %s/swagger/index.html", serverURL(s.listen))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	<-done

	return nil
}

// -- internals ---------------------------------------------------------------

// registerRoutes mounts every route under the configured base path.
//
// The prefix is baked into the patterns rather than applied with a stripping
// middleware, so gin's own Allow computation and route precedence stay intact.
func (s *Server) registerRoutes(r *gin.Engine) {
	b := s.timeouts.BasePath

	r.GET(b+"/", s.handleStatus)
	r.HEAD(b+"/", s.handleStatus)

	r.GET(b+"/install", s.handleInstall)
	r.HEAD(b+"/install", s.handleInstall)
	r.POST(b+"/install", s.handleInstall)

	r.GET(b+"/v1/info", s.handleInfo)
	r.HEAD(b+"/v1/info", s.handleInfo)

	r.POST(b+"/v1/chunks", s.handleUpload)

	r.GET(b+"/v1/chunks/:id", s.handleDownload)
	r.HEAD(b+"/v1/chunks/:id", s.handleDownload)
	r.DELETE(b+"/v1/chunks/:id", s.handleDelete)

	// A real registered route, so it never falls through to the 404 handler.
	r.GET(b+"/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
}

// requireInstalled answers 503 until this server has a config of its own.
//
// It reports whether the caller may continue. A machine client gets the uniform
// error shape rather than a page.
func (s *Server) requireInstalled(c *gin.Context) bool {
	if s.now().installed {
		return true
	}

	writeError(c, http.StatusServiceUnavailable, "server not installed")

	return false
}

// serverURL turns a listen address into something clickable in a log line.
func serverURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		if name, err := os.Hostname(); err == nil && name != "" {
			host = name
		} else {
			host = "localhost"
		}
	}

	return "http://" + net.JoinHostPort(host, port)
}
