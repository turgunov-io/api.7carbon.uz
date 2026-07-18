// Package handlers contains the HTTP layer: the App type that carries shared
// dependencies, every route handler, and the route table itself.
package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// App holds shared dependencies for every handler.
type App struct {
	DB *sql.DB
}

// New builds an App around the given database handle.
func New(db *sql.DB) *App {
	return &App{DB: db}
}

// Request deadlines and admin storage limits.
const (
	healthTimeout       = 5 * time.Second
	readTimeout         = 10 * time.Second
	writeTimeout        = 12 * time.Second
	adminListTimeout    = 20 * time.Second
	adminMetaTimeout    = 5 * time.Second
	storageUploadMaxMB  = 25
	defaultStorageLimit = 50
	maxStorageLimit     = 500
)

// Routes builds the mux with every endpoint registered.
func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", a.rootHandler)
	mux.HandleFunc("/healthz", a.healthHandler)

	// Public content.
	mux.HandleFunc("/contact", a.contactHandler)
	mux.HandleFunc("/about", a.aboutHandler)
	mux.HandleFunc("/banners", a.bannersHandler)
	mux.HandleFunc("/partners", a.partnersHandler)
	mux.HandleFunc("/tuning", a.tuningHandler)
	mux.HandleFunc("/accessories", a.accessoriesHandler)
	mux.HandleFunc("/service_offerings", a.serviceOfferingsHandler)
	mux.HandleFunc("/privacy_sections", a.privacySectionsHandler)
	mux.HandleFunc("/portfolio_items", a.portfolioItemsHandler)
	mux.HandleFunc("/work_post", a.workPostHandler)
	mux.HandleFunc("/api/contacts", a.contactsCRUDHandler)
	mux.HandleFunc("/api/contacts/", a.contactsCRUDHandler)
	mux.HandleFunc("/api/consultations", a.consultationsHandler)

	// Admin.
	mux.HandleFunc("/admin/auth/login", a.adminAuthLoginHandler)
	mux.HandleFunc("/admin/auth/me", a.adminAuthMeHandler)
	a.registerAdminCRUDRoutes(mux)
	mux.HandleFunc("/admin/storage/upload", a.adminStorageUploadHandler)
	mux.HandleFunc("/admin/storage/files", a.adminStorageListHandler)
	mux.HandleFunc("/admin/storage/file", a.adminStorageDeleteHandler)

	return mux
}

func (a *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()

	if err := a.DB.PingContext(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","db":true}`))
}

func (a *App) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"service":"carbon_go","status":"running","routes":["/","/healthz","/contact","/about","/banners","/partners","/tuning","/accessories","/service_offerings","/privacy_sections","/api/contacts","/api/consultations","/portfolio_items","/work_post","/admin/auth/*","/admin/*","/admin/storage/*"]}`))
}
