package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

// App holds shared dependencies.
type App struct {
	DB *sql.DB
}

var contactsAPIConfig = tableCRUDConfig{
	Path:             "/api/contacts",
	Table:            "public.contact",
	OrderBy:          "t.id ASC",
	MutableColumns:   columnSet("phone_number", "address", "description", "email", "work_schedule"),
	RequiredOnCreate: columnSet(),
}

var phonePattern = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
var adminOrderClausePattern = regexp.MustCompile(`^t\.([a-zA-Z_][a-zA-Z0-9_]*)\s+(ASC|DESC)$`)
var loggedAdminOrderFallbacks sync.Map
var cachedAdminTableColumns sync.Map

const (
	healthTimeout       = 5 * time.Second
	readTimeout         = 10 * time.Second
	writeTimeout        = 12 * time.Second
	adminListTimeout    = 20 * time.Second
	adminMetaTimeout    = 5 * time.Second
	storageUploadMaxMB  = 25
	defaultStorageLimit = 50
	maxStorageLimit     = 500
	defaultAdminSession = 12 * time.Hour
	maxAdminSession     = 7 * 24 * time.Hour
	adminTokenPrefix    = "cgadm1"
)

func main() {
	if err := loadDotEnv(); err != nil {
		log.Printf("warning: could not load .env: %v", err)
	}

	dsn := firstNonEmpty(
		os.Getenv("DATABASE_URL"),
		os.Getenv("POSTGRES_DSN"),
	)
	if dsn == "" {
		log.Fatal("DATABASE_URL or POSTGRES_DSN must be set")
	}
	dsn = normalizeDSN(dsn)

	db, err := openDB(dsn)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer db.Close()

	app := &App{DB: db}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.rootHandler)
	mux.HandleFunc("/healthz", app.healthHandler)
	mux.HandleFunc("/contact", app.contactHandler)
	mux.HandleFunc("/about", app.aboutHandler)
	mux.HandleFunc("/banners", app.bannersHandler)
	mux.HandleFunc("/partners", app.partnersHandler)
	mux.HandleFunc("/tuning", app.tuningHandler)
	mux.HandleFunc("/service_offerings", app.serviceOfferingsHandler)
	mux.HandleFunc("/privacy_sections", app.privacySectionsHandler)
	mux.HandleFunc("/api/contacts", app.contactsCRUDHandler)
	mux.HandleFunc("/api/contacts/", app.contactsCRUDHandler)
	mux.HandleFunc("/api/consultations", app.consultationsHandler)
	mux.HandleFunc("/portfolio_items", app.portfolioItemsHandler)
	mux.HandleFunc("/work_post", app.workPostHandler)
	mux.HandleFunc("/admin/auth/login", app.adminAuthLoginHandler)
	mux.HandleFunc("/admin/auth/me", app.adminAuthMeHandler)
	app.registerAdminCRUDRoutes(mux)
	mux.HandleFunc("/admin/storage/upload", app.adminStorageUploadHandler)
	mux.HandleFunc("/admin/storage/files", app.adminStorageListHandler)
	mux.HandleFunc("/admin/storage/file", app.adminStorageDeleteHandler)

	server := &http.Server{
		Addr:         ":" + firstNonEmpty(os.Getenv("PORT"), "8080"),
		Handler:      loggingMiddleware(corsMiddleware(mux)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start the HTTP server.
	go func() {
		log.Printf("listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	if err := db.Close(); err != nil {
		log.Printf("database close error: %v", err)
	}
	log.Println("shutdown complete")
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

func (a *App) bannersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	queries := []string{
		`SELECT id, section, title, COALESCE(to_jsonb(b)->>'image_url', to_jsonb(b)->>'image') AS image_url, priority
		FROM public.banners b
		ORDER BY priority ASC, id ASC`,
		`SELECT id, section, title, COALESCE(to_jsonb(b)->>'image_url', to_jsonb(b)->>'image') AS image_url, priority
		FROM banners b
		ORDER BY priority ASC, id ASC`,
	}

	var rows *sql.Rows
	var err error
	for _, query := range queries {
		rows, err = a.DB.QueryContext(ctx, query)
		if err == nil {
			break
		}
	}
	if err != nil {
		log.Printf("banners query failed: %v", err)
		http.Error(w, "failed to fetch banners", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type banner struct {
		ID       int    `json:"id"`
		Section  string `json:"section"`
		Title    string `json:"title"`
		ImageURL string `json:"image_url"`
		Priority int    `json:"priority"`
	}

	banners := make([]banner, 0, 8)
	for rows.Next() {
		var b banner
		if err := rows.Scan(&b.ID, &b.Section, &b.Title, &b.ImageURL, &b.Priority); err != nil {
			http.Error(w, "failed to read banners", http.StatusInternalServerError)
			return
		}
		banners = append(banners, b)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read banners", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(banners); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) contactHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT id, phone_number, address, description, email, work_schedule
		FROM public.contact
		ORDER BY id ASC`,
	)
	if err != nil {
		rows, err = a.DB.QueryContext(
			ctx,
			`SELECT id, phone_number, address, description, NULL::text AS email, NULL::text AS work_schedule
			FROM public.contact_page
			ORDER BY id ASC`,
		)
	}
	if err != nil {
		http.Error(w, "failed to fetch contact", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type contact struct {
		ID           int     `json:"id"`
		PhoneNumber  *string `json:"phone_number"`
		Address      *string `json:"address"`
		Description  *string `json:"description"`
		Email        *string `json:"email"`
		WorkSchedule *string `json:"work_schedule"`
	}

	contacts := make([]contact, 0, 4)
	for rows.Next() {
		var c contact
		var phoneNumber sql.NullString
		var address sql.NullString
		var description sql.NullString
		var email sql.NullString
		var workSchedule sql.NullString

		if err := rows.Scan(
			&c.ID,
			&phoneNumber,
			&address,
			&description,
			&email,
			&workSchedule,
		); err != nil {
			http.Error(w, "failed to read contact", http.StatusInternalServerError)
			return
		}

		c.PhoneNumber = nullableString(phoneNumber)
		c.Address = nullableString(address)
		c.Description = nullableString(description)
		c.Email = nullableString(email)
		c.WorkSchedule = nullableString(workSchedule)

		contacts = append(contacts, c)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(contacts); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) contactsCRUDHandler(w http.ResponseWriter, r *http.Request) {
	id, hasID, err := parseResourceID(r, contactsAPIConfig.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid id",
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if hasID {
			a.adminFetchOne(w, r, contactsAPIConfig, id)
			return
		}
		a.adminFetchMany(w, r, contactsAPIConfig)
	case http.MethodPost:
		if !requireAdminToken(w, r) {
			return
		}
		a.adminCreateOne(w, r, contactsAPIConfig)
	case http.MethodPut, http.MethodPatch:
		if !requireAdminToken(w, r) {
			return
		}
		if !hasID {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": "id is required",
			})
			return
		}
		a.adminUpdateOne(w, r, contactsAPIConfig, id)
	case http.MethodDelete:
		if !requireAdminToken(w, r) {
			return
		}
		if !hasID {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": "id is required",
			})
			return
		}
		a.adminDeleteOne(w, r, contactsAPIConfig, id)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
	}
}

func (a *App) aboutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	type aboutPage struct {
		ID                 int     `json:"id"`
		Title              *string `json:"title"`
		BannerImageURL     *string `json:"banner_image_url"`
		IntroDescription   *string `json:"intro_description"`
		MissionDescription *string `json:"mission_description"`
		VideoURL           *string `json:"video_url"`
		MissionImageURL    *string `json:"mission_image_url"`
	}
	type aboutMetric struct {
		ID       int    `json:"id"`
		Key      string `json:"key"`
		Value    string `json:"value"`
		Label    string `json:"label"`
		Position int    `json:"position"`
	}
	type aboutSection struct {
		ID          int    `json:"id"`
		Key         string `json:"key"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Position    int    `json:"position"`
	}
	type aboutResponse struct {
		Page     *aboutPage     `json:"page"`
		Metrics  []aboutMetric  `json:"metrics"`
		Sections []aboutSection `json:"sections"`
	}

	page := (*aboutPage)(nil)
	metrics := make([]aboutMetric, 0, 4)
	sections := make([]aboutSection, 0, 4)

	hasAboutPage, err := hasTable(ctx, a.DB, "about_page")
	if err != nil {
		http.Error(w, "failed to resolve about page table", http.StatusInternalServerError)
		return
	}
	if hasAboutPage {
		var pageItem aboutPage
		var title sql.NullString
		var bannerImageURL sql.NullString
		var introDescription sql.NullString
		var missionDescription sql.NullString
		var videoURL sql.NullString
		var missionImageURL sql.NullString

		err := a.DB.QueryRowContext(
			ctx,
			`SELECT id, banner_title, banner_image_url, history_description, mission_description, video_url, mission_image_url
			FROM public.about_page
			ORDER BY id ASC
			LIMIT 1`,
		).Scan(
			&pageItem.ID,
			&title,
			&bannerImageURL,
			&introDescription,
			&missionDescription,
			&videoURL,
			&missionImageURL,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "failed to fetch about page", http.StatusInternalServerError)
			return
		}
		if err == nil {
			pageItem.Title = nullableString(title)
			pageItem.BannerImageURL = nullableString(bannerImageURL)
			pageItem.IntroDescription = nullableString(introDescription)
			pageItem.MissionDescription = nullableString(missionDescription)
			pageItem.VideoURL = nullableString(videoURL)
			pageItem.MissionImageURL = nullableString(missionImageURL)
			page = &pageItem
		}
	}

	aboutID := 1
	if page != nil {
		aboutID = page.ID
	}

	hasAboutMetrics, err := hasTable(ctx, a.DB, "about_metrics")
	if err != nil {
		http.Error(w, "failed to resolve about metrics table", http.StatusInternalServerError)
		return
	}
	if hasAboutMetrics {
		rows, err := a.DB.QueryContext(
			ctx,
			`SELECT id, metric_key, metric_value, metric_label, position
			FROM public.about_metrics
			WHERE about_id = $1
			ORDER BY position ASC, id ASC`,
			aboutID,
		)
		if err != nil {
			http.Error(w, "failed to fetch about metrics", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var item aboutMetric
			if err := rows.Scan(&item.ID, &item.Key, &item.Value, &item.Label, &item.Position); err != nil {
				http.Error(w, "failed to read about metrics", http.StatusInternalServerError)
				return
			}
			metrics = append(metrics, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read about metrics", http.StatusInternalServerError)
			return
		}
	}

	hasAboutSections, err := hasTable(ctx, a.DB, "about_sections")
	if err != nil {
		http.Error(w, "failed to resolve about sections table", http.StatusInternalServerError)
		return
	}
	if hasAboutSections {
		rows, err := a.DB.QueryContext(
			ctx,
			`SELECT id, section_key, title, description, position
			FROM public.about_sections
			WHERE about_id = $1
			ORDER BY position ASC, id ASC`,
			aboutID,
		)
		if err != nil {
			http.Error(w, "failed to fetch about sections", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var item aboutSection
			if err := rows.Scan(&item.ID, &item.Key, &item.Title, &item.Description, &item.Position); err != nil {
				http.Error(w, "failed to read about sections", http.StatusInternalServerError)
				return
			}
			sections = append(sections, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read about sections", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusOK, aboutResponse{
		Page:     page,
		Metrics:  metrics,
		Sections: sections,
	})
}

func (a *App) partnersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT id, logo_url FROM public.partners ORDER BY id ASC`,
	)
	if err != nil {
		http.Error(w, "failed to fetch partners", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type partner struct {
		ID      int    `json:"id"`
		LogoURL string `json:"logo_url"`
	}

	partners := make([]partner, 0, 8)
	for rows.Next() {
		var p partner
		if err := rows.Scan(&p.ID, &p.LogoURL); err != nil {
			http.Error(w, "failed to read partners", http.StatusInternalServerError)
			return
		}
		partners = append(partners, p)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read partners", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(partners); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) tuningHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	queries := []string{
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, NULL::text AS title, card_image_url, full_image_url, description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tuning t
		ORDER BY created_at DESC, id DESC`,
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, title, card_image_url, full_image_url, title AS description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tuning t
		ORDER BY created_at DESC, id DESC`,
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, NULL::text AS title, card_image_url, NULL::jsonb AS full_image_url, description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tuning t
		ORDER BY created_at DESC, id DESC`,
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, title, card_image_url, NULL::jsonb AS full_image_url, title AS description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tuning t
		ORDER BY created_at DESC, id DESC`,
		`SELECT row_number() OVER () AS id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, NULL::text AS title, card_image_url, NULL::jsonb AS full_image_url, description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, NOW() AS created_at, NOW() AS updated_at
		FROM public.tuning t`,
		`SELECT row_number() OVER () AS id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, title, card_image_url, NULL::jsonb AS full_image_url, title AS description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, NOW() AS created_at, NOW() AS updated_at
		FROM public.tuning t`,
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, NULL::text AS title, card_image_url, full_image_url, description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tunning t
		ORDER BY created_at DESC, id DESC`,
		`SELECT id, to_jsonb(t)->>'brand' AS brand, to_jsonb(t)->>'model' AS model, title, card_image_url, full_image_url, title AS description, card_description, full_description, video_image_url, video_link, to_jsonb(t)->>'price' AS price, created_at, updated_at
		FROM public.tunning t
		ORDER BY created_at DESC, id DESC`,
	}

	var rows *sql.Rows
	var err error
	for _, query := range queries {
		rows, err = a.DB.QueryContext(ctx, query)
		if err == nil {
			break
		}
	}
	if err != nil {
		http.Error(w, "failed to fetch tuning", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type tuningItem struct {
		ID              int       `json:"id"`
		Brand           *string   `json:"brand"`
		Model           *string   `json:"model"`
		Title           *string   `json:"title"`
		CardImageURL    *string   `json:"card_image_url"`
		FullImageURL    []string  `json:"full_image_url"`
		Price           *string   `json:"price"`
		Description     *string   `json:"description"`
		CardDescription *string   `json:"card_description"`
		FullDescription *string   `json:"full_description"`
		VideoImageURL   *string   `json:"video_image_url"`
		VideoLink       *string   `json:"video_link"`
		CreatedAt       time.Time `json:"created_at"`
		UpdatedAt       time.Time `json:"updated_at"`
	}

	items := make([]tuningItem, 0, 8)
	for rows.Next() {
		var item tuningItem
		var brand sql.NullString
		var model sql.NullString
		var title sql.NullString
		var cardImageURL sql.NullString
		var fullImageURLRaw []byte
		var price sql.NullString
		var description sql.NullString
		var cardDescription sql.NullString
		var fullDescription sql.NullString
		var videoImageURL sql.NullString
		var videoLink sql.NullString

		if err := rows.Scan(
			&item.ID,
			&brand,
			&model,
			&title,
			&cardImageURL,
			&fullImageURLRaw,
			&description,
			&cardDescription,
			&fullDescription,
			&videoImageURL,
			&videoLink,
			&price,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			http.Error(w, "failed to read tuning", http.StatusInternalServerError)
			return
		}

		item.Brand = nullableString(brand)
		item.Model = nullableString(model)
		item.Title = nullableString(title)
		item.CardImageURL = nullableString(cardImageURL)
		item.FullImageURL = parseStringArray(fullImageURLRaw)
		item.Price = nullableString(price)
		item.Description = nullableString(description)
		item.CardDescription = nullableString(cardDescription)
		item.FullDescription = nullableString(fullDescription)
		item.VideoImageURL = nullableString(videoImageURL)
		item.VideoLink = nullableString(videoLink)

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read tuning", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) portfolioItemsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT id, brand, title, image_url, description, youtube_link, created_at
		FROM public.portfolio_items
		ORDER BY created_at DESC, id DESC`,
	)
	if err != nil {
		http.Error(w, "failed to fetch portfolio items", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type portfolioItem struct {
		ID          int       `json:"id"`
		Brand       *string   `json:"brand"`
		Title       string    `json:"title"`
		ImageURL    string    `json:"image_url"`
		Description *string   `json:"description"`
		YoutubeLink *string   `json:"youtube_link"`
		CreatedAt   time.Time `json:"created_at"`
	}

	items := make([]portfolioItem, 0, 8)
	for rows.Next() {
		var item portfolioItem
		var brand sql.NullString
		var description sql.NullString
		var youtubeLink sql.NullString

		if err := rows.Scan(
			&item.ID,
			&brand,
			&item.Title,
			&item.ImageURL,
			&description,
			&youtubeLink,
			&item.CreatedAt,
		); err != nil {
			http.Error(w, "failed to read portfolio items", http.StatusInternalServerError)
			return
		}

		item.Brand = nullableString(brand)
		item.Description = nullableString(description)
		item.YoutubeLink = nullableString(youtubeLink)

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read portfolio items", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) workPostHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	tableName, err := resolveWorkPostTable(ctx, a.DB)
	if err != nil {
		http.Error(w, "failed to resolve work posts table", http.StatusInternalServerError)
		return
	}
	if tableName == "" {
		http.Error(w, "work posts table not found", http.StatusInternalServerError)
		return
	}

	hasGalleryImages, err := hasColumn(ctx, a.DB, tableName, "gallery_images")
	if err != nil {
		http.Error(w, "failed to resolve work posts columns", http.StatusInternalServerError)
		return
	}

	query := `SELECT id, title_model, card_image_url, full_image_url, card_description, work_list, full_description, video_image_url, video_link, NULL::jsonb AS gallery_images, created_at, updated_at
		FROM public.blog_posts
		ORDER BY created_at DESC, id DESC`
	if tableName == "work_post" && hasGalleryImages {
		query = `SELECT id, title_model, card_image_url, full_image_url, card_description, work_list, full_description, video_image_url, video_link, gallery_images, created_at, updated_at
		FROM public.work_post
		ORDER BY created_at DESC, id DESC`
	} else if tableName == "work_post" {
		query = `SELECT id, title_model, card_image_url, full_image_url, card_description, work_list, full_description, video_image_url, video_link, NULL::jsonb AS gallery_images, created_at, updated_at
		FROM public.work_post
		ORDER BY created_at DESC, id DESC`
	} else if tableName == "blog_posts" && hasGalleryImages {
		query = `SELECT id, title_model, card_image_url, full_image_url, card_description, work_list, full_description, video_image_url, video_link, gallery_images, created_at, updated_at
		FROM public.blog_posts
		ORDER BY created_at DESC, id DESC`
	}

	rows, err := a.DB.QueryContext(
		ctx,
		query,
	)
	if err != nil {
		http.Error(w, "failed to fetch work posts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type workPost struct {
		ID              int      `json:"id"`
		Title           string   `json:"title"`
		Description     string   `json:"description"`
		FullDescription string   `json:"fullDescription"`
		ImageURL        string   `json:"imageUrl"`
		VideoURL        string   `json:"videoUrl"`
		PerformedWorks  []string `json:"performedWorks"`
		GalleryImages   []string `json:"galleryImages"`
	}

	posts := make([]workPost, 0, 8)
	for rows.Next() {
		var post workPost
		var titleModel string
		var cardImageURL sql.NullString
		var fullImageURL sql.NullString
		var cardDescription sql.NullString
		var workList []byte
		var fullDescription sql.NullString
		var videoImageURL sql.NullString
		var videoLink sql.NullString
		var galleryImagesRaw []byte
		var createdAt time.Time
		var updatedAt time.Time

		if err := rows.Scan(
			&post.ID,
			&titleModel,
			&cardImageURL,
			&fullImageURL,
			&cardDescription,
			&workList,
			&fullDescription,
			&videoImageURL,
			&videoLink,
			&galleryImagesRaw,
			&createdAt,
			&updatedAt,
		); err != nil {
			http.Error(w, "failed to read work posts", http.StatusInternalServerError)
			return
		}

		cardURL := nullStringValue(cardImageURL)
		fullURL := nullStringValue(fullImageURL)
		videoImage := nullStringValue(videoImageURL)

		post.Title = titleModel
		post.Description = nullStringValue(cardDescription)
		post.FullDescription = nullStringValue(fullDescription)
		post.ImageURL = firstNonEmpty(cardURL, fullURL, videoImage)
		post.VideoURL = nullStringValue(videoLink)
		post.PerformedWorks = parsePerformedWorks(workList)
		post.GalleryImages = parseStringArray(galleryImagesRaw)
		if len(post.GalleryImages) == 0 {
			post.GalleryImages = uniqueNonEmpty(cardURL, fullURL, videoImage)
		}

		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read work posts", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(posts); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) serviceOfferingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	queries := []string{
		`SELECT id, service_type, title, detailed_description, gallery_images, price_text, position, created_at, updated_at
		FROM public.service_offerings
		ORDER BY position ASC, id ASC`,
		`SELECT id, service_type, title, detailed_description, gallery_images, price_text, position, NOW() AS created_at, NOW() AS updated_at
		FROM public.service_offerings
		ORDER BY position ASC, id ASC`,
		`SELECT id, service_type, title, detailed_description, NULL::jsonb AS gallery_images, price_text, position, NOW() AS created_at, NOW() AS updated_at
		FROM public.service_offerings
		ORDER BY position ASC, id ASC`,
	}

	var rows *sql.Rows
	var err error
	for _, query := range queries {
		rows, err = a.DB.QueryContext(ctx, query)
		if err == nil {
			break
		}
	}
	if err != nil {
		http.Error(w, "failed to fetch service offerings", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type serviceOffering struct {
		ID                  int       `json:"id"`
		ServiceType         *string   `json:"service_type"`
		Title               *string   `json:"title"`
		DetailedDescription *string   `json:"detailed_description"`
		GalleryImages       []string  `json:"gallery_images"`
		PriceText           *string   `json:"price_text"`
		Position            int       `json:"position"`
		CreatedAt           time.Time `json:"created_at"`
		UpdatedAt           time.Time `json:"updated_at"`
	}

	items := make([]serviceOffering, 0, 8)
	for rows.Next() {
		var item serviceOffering
		var serviceType sql.NullString
		var title sql.NullString
		var detailedDescription sql.NullString
		var galleryImagesRaw []byte
		var priceText sql.NullString

		if err := rows.Scan(
			&item.ID,
			&serviceType,
			&title,
			&detailedDescription,
			&galleryImagesRaw,
			&priceText,
			&item.Position,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			http.Error(w, "failed to read service offerings", http.StatusInternalServerError)
			return
		}

		item.ServiceType = nullableString(serviceType)
		item.Title = nullableString(title)
		item.DetailedDescription = nullableString(detailedDescription)
		item.GalleryImages = parseStringArray(galleryImagesRaw)
		item.PriceText = nullableString(priceText)

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read service offerings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) privacySectionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT id, title, description, position
		FROM public.privacy_sections
		ORDER BY position ASC, id ASC`,
	)
	if err != nil {
		http.Error(w, "failed to fetch privacy sections", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type privacySection struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Position    int    `json:"position"`
	}

	items := make([]privacySection, 0, 8)
	for rows.Next() {
		var item privacySection
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Position); err != nil {
			http.Error(w, "failed to read privacy sections", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read privacy sections", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *App) consultationsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.createConsultationHandler(w, r)
	case http.MethodGet:
		a.listConsultationsHandler(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "?????????? ???? ????????????????????????????",
		})
	}
}

func (a *App) createConsultationHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB safety limit for JSON payload
	defer r.Body.Close()

	type consultationCreateRequest struct {
		FirstName         string `json:"first_name"`
		LastName          string `json:"last_name"`
		Phone             string `json:"phone"`
		ServiceType       string `json:"service_type"`
		CarModel          string `json:"car_model"`
		PreferredCallTime string `json:"preferred_call_time"`
		Comments          string `json:"comments"`
	}

	var req consultationCreateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "???????????????????????? JSON",
			"errors": map[string]string{
				"body": "?????????????????? ???????????? ??????????????",
			},
		})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "???????????????????????? JSON",
			"errors": map[string]string{
				"body": "?????????????????? ???????? JSON-????????????",
			},
		})
		return
	}

	errorsMap := validateConsultationRequest(
		req.FirstName,
		req.LastName,
		req.Phone,
		req.ServiceType,
		req.CarModel,
		req.PreferredCallTime,
		req.Comments,
	)
	if len(errorsMap) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"status":  "error",
			"message": "???????????? ??????????????????",
			"errors":  errorsMap,
		})
		return
	}

	firstName := strings.TrimSpace(req.FirstName)
	lastName := strings.TrimSpace(req.LastName)
	phone := strings.TrimSpace(req.Phone)
	serviceType := strings.TrimSpace(req.ServiceType)
	carModel := optionalStringDBValue(req.CarModel)
	preferredCallTime := optionalStringDBValue(req.PreferredCallTime)
	comments := optionalStringDBValue(req.Comments)

	var id int64
	var createdAt time.Time
	err := a.DB.QueryRowContext(
		ctx,
		`INSERT INTO public.consultations
		(first_name, last_name, phone, service_type, car_model, preferred_call_time, comments, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'new')
		RETURNING id, created_at`,
		firstName,
		lastName,
		phone,
		serviceType,
		carModel,
		preferredCallTime,
		comments,
	).Scan(&id, &createdAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "???? ?????????????? ?????????????????? ????????????",
		})
		return
	}

	go notifyAdminAboutConsultation(consultationNotification{
		ID:                id,
		FirstName:         firstName,
		LastName:          lastName,
		Phone:             phone,
		ServiceType:       serviceType,
		CarModel:          optionalStringValue(req.CarModel),
		PreferredCallTime: optionalStringValue(req.PreferredCallTime),
		Comments:          optionalStringValue(req.Comments),
		Status:            "new",
		CreatedAt:         createdAt,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":  "success",
		"message": "???????????? ?????????????? ??????????????",
		"data": map[string]any{
			"id":         id,
			"created_at": createdAt.UTC().Format(time.RFC3339),
		},
	})
}

func (a *App) listConsultationsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	query := `SELECT id, first_name, last_name, phone, service_type, car_model, preferred_call_time, comments, status, created_at
		FROM public.consultations`
	args := []any{}
	if statusFilter != "" {
		query += ` WHERE status = $1`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := a.DB.QueryContext(ctx, query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "???? ?????????????? ???????????????? ????????????",
		})
		return
	}
	defer rows.Close()

	type consultationItem struct {
		ID                int64     `json:"id"`
		FirstName         string    `json:"first_name"`
		LastName          string    `json:"last_name"`
		Phone             string    `json:"phone"`
		ServiceType       string    `json:"service_type"`
		CarModel          *string   `json:"car_model"`
		PreferredCallTime *string   `json:"preferred_call_time"`
		Comments          *string   `json:"comments"`
		Status            string    `json:"status"`
		CreatedAt         time.Time `json:"created_at"`
	}

	items := make([]consultationItem, 0, 16)
	for rows.Next() {
		var item consultationItem
		var carModel sql.NullString
		var preferredCallTime sql.NullString
		var comments sql.NullString

		if err := rows.Scan(
			&item.ID,
			&item.FirstName,
			&item.LastName,
			&item.Phone,
			&item.ServiceType,
			&carModel,
			&preferredCallTime,
			&comments,
			&item.Status,
			&item.CreatedAt,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "???? ?????????????? ?????????????????? ????????????",
			})
			return
		}

		item.CarModel = nullableString(carModel)
		item.PreferredCallTime = nullableString(preferredCallTime)
		item.Comments = nullableString(comments)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "???? ?????????????? ?????????????????? ????????????",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   items,
	})
}

// rootHandler gives a friendly response for "/" instead of 404.
func (a *App) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"service":"carbon_go","status":"running","routes":["/","/healthz","/contact","/about","/banners","/partners","/tuning","/service_offerings","/privacy_sections","/api/contacts","/api/consultations","/portfolio_items","/work_post","/admin/auth/*","/admin/*","/admin/storage/*"]}`))
}

type adminAuthLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) adminAuthLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := loadAdminAuthConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}
	if cfg.Username == "" || cfg.Password == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "admin login is not configured (set ADMIN_USERNAME and ADMIN_PASSWORD)",
		})
		return
	}
	if cfg.SigningSecret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "admin login requires JWT_SECRET or ADMIN_JWT_SECRET",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var payload adminAuthLoginRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid JSON body",
		})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "expected a single JSON object",
		})
		return
	}

	username := strings.TrimSpace(payload.Username)
	password := strings.TrimSpace(payload.Password)

	usernameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.Username)) == 1
	passwordMatches := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.Password)) == 1
	if !usernameMatches || !passwordMatches {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"status":  "error",
			"message": "invalid credentials",
		})
		return
	}

	token, expiresAt, err := issueAdminAccessToken(username, cfg.SigningSecret, cfg.SessionTTL, time.Now())
	if err != nil {
		log.Printf("admin auth token issue failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to issue access token",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"token_type":   "Bearer",
			"access_token": token,
			"expires_at":   expiresAt.UTC().Format(time.RFC3339),
			"expires_in":   int(cfg.SessionTTL.Seconds()),
			"username":     username,
		},
	})
}

func (a *App) adminAuthMeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}
	if !requireAdminToken(w, r) {
		return
	}

	data := map[string]any{
		"authenticated": true,
		"auth_type":     "static_token",
	}

	cfg, err := loadAdminAuthConfig()
	if err == nil && cfg.SigningSecret != "" {
		provided := extractAdminToken(r)
		claims, verifyErr := verifyAdminAccessToken(provided, cfg.SigningSecret, time.Now())
		if verifyErr == nil {
			data["auth_type"] = "bearer"
			data["username"] = claims.Username
			data["expires_at"] = time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

type tableCRUDConfig struct {
	Path             string
	Table            string
	OrderBy          string
	MutableColumns   map[string]struct{}
	RequiredOnCreate map[string]struct{}
	JSONColumns      map[string]struct{}
	TouchUpdatedAt   bool
}

func columnSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (a *App) registerAdminCRUDRoutes(mux *http.ServeMux) {
	configs := []tableCRUDConfig{
		{
			Path:             "/admin/banners",
			Table:            "public.banners",
			OrderBy:          "t.priority ASC, t.id ASC",
			MutableColumns:   columnSet("section", "title", "image_url", "priority"),
			RequiredOnCreate: columnSet("section", "title", "image_url"),
		},
		{
			Path:             "/admin/contact",
			Table:            "public.contact",
			OrderBy:          "t.id ASC",
			MutableColumns:   columnSet("phone_number", "address", "description", "email", "work_schedule"),
			RequiredOnCreate: columnSet(),
		},
		{
			Path:             "/admin/contact_page",
			Table:            "public.contact_page",
			OrderBy:          "t.id ASC",
			MutableColumns:   columnSet("id", "phone_number", "address", "description", "image_url"),
			RequiredOnCreate: columnSet(),
		},
		{
			Path:             "/admin/about_page",
			Table:            "public.about_page",
			OrderBy:          "t.id ASC",
			MutableColumns:   columnSet("id", "banner_image_url", "banner_title", "history_description", "video_url", "mission_description", "mission_image_url"),
			RequiredOnCreate: columnSet(),
		},
		{
			Path:             "/admin/about_metrics",
			Table:            "public.about_metrics",
			OrderBy:          "t.position ASC, t.id ASC",
			MutableColumns:   columnSet("about_id", "metric_key", "metric_value", "metric_label", "position"),
			RequiredOnCreate: columnSet("metric_key", "metric_value", "metric_label"),
		},
		{
			Path:             "/admin/about_sections",
			Table:            "public.about_sections",
			OrderBy:          "t.position ASC, t.id ASC",
			MutableColumns:   columnSet("about_id", "section_key", "title", "description", "position"),
			RequiredOnCreate: columnSet("section_key", "title", "description"),
		},
		{
			Path:             "/admin/partners",
			Table:            "public.partners",
			OrderBy:          "t.position ASC, t.id ASC",
			MutableColumns:   columnSet("name", "logo_url", "position"),
			RequiredOnCreate: columnSet("logo_url"),
		},
		{
			Path:             "/admin/tuning",
			Table:            "public.tuning",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("brand", "model", "card_image_url", "full_image_url", "price", "description", "card_description", "full_description", "video_image_url", "video_link"),
			RequiredOnCreate: columnSet(),
			JSONColumns:      columnSet("full_image_url"),
			TouchUpdatedAt:   true,
		},
		{
			Path:             "/admin/service_offerings",
			Table:            "public.service_offerings",
			OrderBy:          "t.position ASC, t.id ASC",
			MutableColumns:   columnSet("service_type", "title", "detailed_description", "gallery_images", "price_text", "position"),
			RequiredOnCreate: columnSet("service_type", "title"),
			JSONColumns:      columnSet("gallery_images"),
			TouchUpdatedAt:   true,
		},
		{
			Path:             "/admin/privacy_sections",
			Table:            "public.privacy_sections",
			OrderBy:          "t.position ASC, t.id ASC",
			MutableColumns:   columnSet("title", "description", "position"),
			RequiredOnCreate: columnSet("title", "description"),
		},
		{
			Path:             "/admin/portfolio_items",
			Table:            "public.portfolio_items",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("brand", "title", "image_url", "description", "youtube_link"),
			RequiredOnCreate: columnSet("title", "image_url"),
		},
		{
			Path:             "/admin/work_post",
			Table:            "public.work_post",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("title_model", "card_image_url", "full_image_url", "card_description", "work_list", "gallery_images", "full_description", "video_image_url", "video_link"),
			RequiredOnCreate: columnSet("title_model"),
			JSONColumns:      columnSet("work_list", "gallery_images"),
			TouchUpdatedAt:   true,
		},
		{
			Path:             "/admin/blog_posts",
			Table:            "public.blog_posts",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("title_model", "card_image_url", "full_image_url", "card_description", "work_list", "gallery_images", "full_description", "video_image_url", "video_link"),
			RequiredOnCreate: columnSet("title_model"),
			JSONColumns:      columnSet("work_list", "gallery_images"),
			TouchUpdatedAt:   true,
		},
		{
			Path:             "/admin/consultations",
			Table:            "public.consultations",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("first_name", "last_name", "phone", "service_type", "car_model", "preferred_call_time", "comments", "status"),
			RequiredOnCreate: columnSet("first_name", "last_name", "phone", "service_type"),
		},
	}

	for _, cfg := range configs {
		config := cfg
		handler := a.makeAdminTableCRUDHandler(config)
		mux.HandleFunc(config.Path, handler)
		mux.HandleFunc(config.Path+"/", handler)
	}
}

func (a *App) makeAdminTableCRUDHandler(cfg tableCRUDConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdminToken(w, r) {
			return
		}

		id, hasID, err := parseResourceID(r, cfg.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": "invalid id",
			})
			return
		}

		switch r.Method {
		case http.MethodGet:
			if hasID {
				a.adminFetchOne(w, r, cfg, id)
				return
			}
			a.adminFetchMany(w, r, cfg)
		case http.MethodPost:
			a.adminCreateOne(w, r, cfg)
		case http.MethodPut, http.MethodPatch:
			if !hasID {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": "id is required",
				})
				return
			}
			a.adminUpdateOne(w, r, cfg, id)
		case http.MethodDelete:
			if !hasID {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": "id is required",
				})
				return
			}
			a.adminDeleteOne(w, r, cfg, id)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"status":  "error",
				"message": "method not allowed",
			})
		}
	}
}

func (a *App) adminFetchMany(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig) {
	ctx, cancel := context.WithTimeout(r.Context(), adminListTimeout)
	defer cancel()

	orderBy := strings.TrimSpace(cfg.OrderBy)
	if orderBy == "" {
		orderBy = "t.id ASC"
	}

	metaCtx, metaCancel := context.WithTimeout(ctx, adminMetaTimeout)
	resolvedOrderBy, err := a.resolveAdminListOrderBy(metaCtx, cfg.Table, orderBy)
	metaCancel()
	if err != nil {
		log.Printf("admin list %s order resolution failed, using configured order '%s': %v", cfg.Table, orderBy, err)
	} else {
		orderBy = resolvedOrderBy
	}

	var raw []byte
	if err := a.queryAdminListWithTimeout(ctx, cfg.Table, orderBy, &raw); err != nil {
		if orderBy != "t.id ASC" {
			log.Printf("admin list %s failed with order '%s': %v; retry with id ASC", cfg.Table, orderBy, err)
			if retryErr := a.queryAdminListWithTimeout(ctx, cfg.Table, "t.id ASC", &raw); retryErr != nil {
				log.Printf("admin list %s retry failed: %v", cfg.Table, retryErr)
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"status":  "error",
					"message": "failed to fetch data",
				})
				return
			}
		} else {
			log.Printf("admin list %s failed: %v", cfg.Table, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to fetch data",
			})
			return
		}
	}

	var data any
	if len(raw) == 0 {
		data = []any{}
	} else if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin list %s decode failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse data",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) resolveAdminListOrderBy(ctx context.Context, tableName, desired string) (string, error) {
	orderBy := strings.TrimSpace(desired)
	if orderBy == "" {
		return "t.id ASC", nil
	}

	columns, err := a.loadAdminTableColumns(ctx, tableName)
	if err != nil {
		return "", err
	}

	clauses := strings.Split(orderBy, ",")
	resolved := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}

		match := adminOrderClausePattern.FindStringSubmatch(clause)
		if len(match) != 3 {
			return "", fmt.Errorf("unsupported order clause %q", clause)
		}

		columnName := match[1]
		if _, exists := columns[columnName]; !exists {
			logAdminOrderFallbackOnce(tableName, clause, columnName)
			continue
		}

		resolved = append(resolved, clause)
	}

	if len(resolved) == 0 {
		return "t.id ASC", nil
	}

	return strings.Join(resolved, ", "), nil
}

func (a *App) queryAdminList(ctx context.Context, tableName, orderBy string, out *[]byte) error {
	query := fmt.Sprintf(
		`SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY %s), '[]'::json) FROM %s t`,
		orderBy,
		quoteTableName(tableName),
	)
	return a.DB.QueryRowContext(ctx, query).Scan(out)
}

func (a *App) queryAdminListWithTimeout(parent context.Context, tableName, orderBy string, out *[]byte) error {
	ctx, cancel := context.WithTimeout(parent, readTimeout)
	defer cancel()
	return a.queryAdminList(ctx, tableName, orderBy, out)
}

func (a *App) adminFetchOne(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	query := fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE t.id = $1`, quoteTableName(cfg.Table))

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin fetch one %s failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to fetch data",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin fetch one %s decode failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse data",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) adminCreateOne(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	payload, ok := decodeJSONMap(w, r)
	if !ok {
		return
	}
	if cfg.Path == "/admin/tuning" {
		normalizeTuningPayload(payload)
	}

	if validationErrors := validateCRUDPayload(payload, cfg.MutableColumns, cfg.RequiredOnCreate); len(validationErrors) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"status":  "error",
			"message": "validation error",
			"errors":  validationErrors,
		})
		return
	}
	if cfg.Path == "/admin/tuning" {
		if err := a.alignTuningPayloadToSchema(ctx, payload); err != nil {
			log.Printf("admin create %s schema alignment failed: %v", cfg.Table, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to prepare payload",
			})
			return
		}
	}

	keys := sortedMapKeys(payload)
	quotedTable := quoteTableName(cfg.Table)

	query := ""
	args := make([]any, 0, len(keys))
	if len(keys) == 0 {
		query = fmt.Sprintf(
			`WITH ins AS (INSERT INTO %s DEFAULT VALUES RETURNING *) SELECT to_jsonb(ins) FROM ins`,
			quotedTable,
		)
	} else {
		columns := make([]string, 0, len(keys))
		placeholders := make([]string, 0, len(keys))
		for idx, key := range keys {
			columns = append(columns, quoteIdentifier(key))
			placeholders = append(placeholders, fmt.Sprintf("$%d", idx+1))

			value, err := normalizeCRUDValue(key, payload[key], cfg)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": err.Error(),
				})
				return
			}
			args = append(args, value)
		}

		query = fmt.Sprintf(
			`WITH ins AS (INSERT INTO %s (%s) VALUES (%s) RETURNING *) SELECT to_jsonb(ins) FROM ins`,
			quotedTable,
			strings.Join(columns, ", "),
			strings.Join(placeholders, ", "),
		)
	}

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		log.Printf("admin create %s failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to create record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin create %s decode failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse created record",
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) adminUpdateOne(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	payload, ok := decodeJSONMap(w, r)
	if !ok {
		return
	}
	if cfg.Path == "/admin/tuning" {
		normalizeTuningPayload(payload)
	}

	if validationErrors := validateCRUDPayload(payload, cfg.MutableColumns, nil); len(validationErrors) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"status":  "error",
			"message": "validation error",
			"errors":  validationErrors,
		})
		return
	}
	if cfg.Path == "/admin/tuning" {
		if err := a.alignTuningPayloadToSchema(ctx, payload); err != nil {
			log.Printf("admin update %s schema alignment failed: %v", cfg.Table, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to prepare payload",
			})
			return
		}
	}

	keys := sortedMapKeys(payload)
	if len(keys) == 0 && !cfg.TouchUpdatedAt {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "empty payload",
		})
		return
	}

	setClauses := make([]string, 0, len(keys)+1)
	args := make([]any, 0, len(keys)+1)
	for idx, key := range keys {
		value, err := normalizeCRUDValue(key, payload[key], cfg)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": err.Error(),
			})
			return
		}
		args = append(args, value)
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdentifier(key), idx+1))
	}
	if cfg.TouchUpdatedAt {
		setClauses = append(setClauses, `updated_at = NOW()`)
	}

	query := fmt.Sprintf(
		`WITH upd AS (UPDATE %s SET %s WHERE id = $%d RETURNING *) SELECT to_jsonb(upd) FROM upd`,
		quoteTableName(cfg.Table),
		strings.Join(setClauses, ", "),
		len(args)+1,
	)
	args = append(args, id)

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin update %s failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to update record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin update %s decode failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse updated record",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) adminDeleteOne(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	query := fmt.Sprintf(
		`WITH del AS (DELETE FROM %s WHERE id = $1 RETURNING *) SELECT to_jsonb(del) FROM del`,
		quoteTableName(cfg.Table),
	)

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin delete %s failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to delete record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin delete %s decode failed: %v", cfg.Table, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse deleted record",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func parseResourceID(r *http.Request, basePath string) (int64, bool, error) {
	if idParam := strings.TrimSpace(r.URL.Query().Get("id")); idParam != "" {
		id, err := strconv.ParseInt(idParam, 10, 64)
		if err != nil {
			return 0, false, err
		}
		return id, true, nil
	}

	base := strings.TrimSuffix(basePath, "/")
	path := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	if path == base {
		return 0, false, nil
	}
	if !strings.HasPrefix(path, base+"/") {
		return 0, false, nil
	}

	idPart := strings.TrimPrefix(path, base+"/")
	if idPart == "" {
		return 0, false, nil
	}
	if strings.Contains(idPart, "/") {
		return 0, false, fmt.Errorf("invalid id path")
	}

	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func decodeJSONMap(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var payload map[string]any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid JSON body",
		})
		return nil, false
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "expected a single JSON object",
		})
		return nil, false
	}

	if payload == nil {
		payload = map[string]any{}
	}
	return payload, true
}

func validateCRUDPayload(payload map[string]any, allowedColumns, requiredColumns map[string]struct{}) map[string]string {
	validationErrors := map[string]string{}

	for key := range payload {
		if _, ok := allowedColumns[key]; !ok {
			validationErrors[key] = "field is not editable"
		}
	}
	for key := range requiredColumns {
		value, ok := payload[key]
		if !ok || isEmptyJSONValue(value) {
			validationErrors[key] = "field is required"
		}
	}

	return validationErrors
}

func isEmptyJSONValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(text) == ""
}

func normalizeTuningPayload(payload map[string]any) {
	title, hasTitle := payload["title"]
	description, hasDescription := payload["description"]

	if !hasDescription && hasTitle {
		payload["description"] = title
	} else if isEmptyJSONValue(description) && hasTitle && !isEmptyJSONValue(title) {
		payload["description"] = title
	}

	// Treat `title` as an input alias; final write columns are resolved by schema.
	delete(payload, "title")
}

func (a *App) alignTuningPayloadToSchema(ctx context.Context, payload map[string]any) error {
	hasTitleColumn, err := hasColumn(ctx, a.DB, "tuning", "title")
	if err != nil {
		return err
	}
	hasDescriptionColumn, err := hasColumn(ctx, a.DB, "tuning", "description")
	if err != nil {
		return err
	}

	description, hasDescription := payload["description"]
	if hasTitleColumn && hasDescription {
		payload["title"] = description
	} else {
		delete(payload, "title")
	}
	if !hasDescriptionColumn {
		delete(payload, "description")
	}

	return nil
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeCRUDValue(column string, value any, cfg tableCRUDConfig) (any, error) {
	if _, ok := cfg.JSONColumns[column]; ok {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON value for %s", column)
		}
		return string(raw), nil
	}

	number, ok := value.(float64)
	if ok && number == float64(int64(number)) {
		return int64(number), nil
	}

	return value, nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteTableName(value string) string {
	parts := strings.Split(value, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, quoteIdentifier(strings.TrimSpace(part)))
	}
	return strings.Join(quoted, ".")
}

func baseTableName(value string) string {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"`)
}

func logAdminOrderFallbackOnce(tableName, clause, columnName string) {
	key := tableName + "|" + columnName + "|" + clause
	if _, loaded := loggedAdminOrderFallbacks.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("admin list %s: dropping order clause %q because column %s does not exist", tableName, clause, columnName)
}

func (a *App) loadAdminTableColumns(ctx context.Context, tableName string) (map[string]struct{}, error) {
	if cached, ok := cachedAdminTableColumns.Load(tableName); ok {
		return cached.(map[string]struct{}), nil
	}

	baseTable := baseTableName(tableName)
	if baseTable == "" {
		return nil, fmt.Errorf("invalid table name %q", tableName)
	}

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1`,
		baseTable,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]struct{}{}
	for rows.Next() {
		var columnName string
		if err := rows.Scan(&columnName); err != nil {
			return nil, err
		}
		columns[columnName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cachedAdminTableColumns.Store(tableName, columns)
	return columns, nil
}

type supabaseStorageConfig struct {
	BaseURL       string
	ServiceRole   string
	DefaultBucket string
}

func (a *App) adminStorageUploadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminToken(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := loadSupabaseStorageConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	maxBytes := int64(storageUploadMaxMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid multipart form",
		})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "file is required (form-data key: file)",
		})
		return
	}
	defer file.Close()

	bucketValue := firstNonEmpty(r.FormValue("bucket"), cfg.DefaultBucket)
	bucket, err := cleanStorageBucket(bucketValue)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	filename := sanitizeFilename(firstNonEmpty(r.FormValue("filename"), fileHeader.Filename))
	if filename == "" {
		filename = fmt.Sprintf("upload_%d.bin", time.Now().UnixNano())
	}

	folder, err := cleanOptionalStoragePath(r.FormValue("folder"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	objectPath := filename
	if folder != "" {
		objectPath = folder + "/" + filename
	}

	contentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	upsertValue := strings.TrimSpace(strings.ToLower(r.FormValue("upsert")))
	upsert := upsertValue == "" || upsertValue == "1" || upsertValue == "true" || upsertValue == "yes"

	uploadURL := buildSupabaseObjectURL(cfg.BaseURL, bucket, objectPath)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to build upload request",
		})
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)
	req.Header.Set("x-upsert", strconv.FormatBool(upsert))
	req.Header.Set("Content-Type", contentType)
	if fileHeader.Size > 0 {
		req.ContentLength = fileHeader.Size
	}

	resp, err := (&http.Client{Timeout: 70 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage upload request failed",
		})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage upload failed",
			"details": strings.TrimSpace(string(respBody)),
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "success",
		"data": map[string]any{
			"bucket":        bucket,
			"path":          objectPath,
			"mime_type":     contentType,
			"size":          fileHeader.Size,
			"upsert":        upsert,
			"storage_url":   buildSupabaseObjectURL(cfg.BaseURL, bucket, objectPath),
			"public_url":    buildSupabasePublicURL(cfg.BaseURL, bucket, objectPath),
			"storage_reply": strings.TrimSpace(string(respBody)),
		},
	})
}

func (a *App) adminStorageListHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminToken(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := loadSupabaseStorageConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	prefix, err := cleanOptionalStoragePath(r.URL.Query().Get("prefix"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	limit, err := parseIntOrDefault(r.URL.Query().Get("limit"), defaultStorageLimit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "limit must be an integer",
		})
		return
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxStorageLimit {
		limit = maxStorageLimit
	}

	offset, err := parseIntOrDefault(r.URL.Query().Get("offset"), 0)
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "offset must be a non-negative integer",
		})
		return
	}

	sortColumn := firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("sort_column")), "name")
	sortOrder := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort_order")))
	if sortOrder != "desc" {
		sortOrder = "asc"
	}

	opts := storageListOptions{
		Prefix:     prefix,
		Limit:      limit,
		Offset:     offset,
		SortColumn: sortColumn,
		SortOrder:  sortOrder,
		Search:     strings.TrimSpace(r.URL.Query().Get("search")),
	}

	allBuckets := isTruthy(r.URL.Query().Get("all"))
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	if allBuckets {
		buckets, err := a.supabaseListBuckets(ctx, cfg)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"status":  "error",
				"message": "failed to fetch buckets",
				"details": err.Error(),
			})
			return
		}

		items := make(map[string]any, len(buckets))
		for _, bucket := range buckets {
			data, err := a.supabaseListBucketFiles(ctx, cfg, bucket, opts)
			if err != nil {
				items[bucket] = map[string]any{
					"status":  "error",
					"message": err.Error(),
				}
				continue
			}
			items[bucket] = data
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success",
			"data":   items,
			"meta": map[string]any{
				"all":            true,
				"bucket_count":   len(buckets),
				"per_bucket_max": opts.Limit,
				"prefix":         opts.Prefix,
			},
		})
		return
	}

	bucketValue := firstNonEmpty(r.URL.Query().Get("bucket"), cfg.DefaultBucket)
	bucket, err := cleanStorageBucket(bucketValue)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	data, err := a.supabaseListBucketFiles(ctx, cfg, bucket, opts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage list failed",
			"details": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
		"meta": map[string]any{
			"bucket": bucket,
			"prefix": prefix,
			"limit":  limit,
			"offset": offset,
		},
	})
}

type storageListOptions struct {
	Prefix     string
	Limit      int
	Offset     int
	SortColumn string
	SortOrder  string
	Search     string
}

func (a *App) supabaseListBuckets(ctx context.Context, cfg supabaseStorageConfig) ([]string, error) {
	listURL := fmt.Sprintf("%s/storage/v1/bucket", strings.TrimRight(cfg.BaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build buckets request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("buckets request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("buckets request status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode buckets response: %w", err)
	}

	buckets := make([]string, 0, len(payload))
	for _, bucket := range payload {
		name, _ := bucket["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		buckets = append(buckets, name)
	}
	sort.Strings(buckets)
	return buckets, nil
}

func (a *App) supabaseListBucketFiles(ctx context.Context, cfg supabaseStorageConfig, bucket string, opts storageListOptions) (any, error) {
	payload := map[string]any{
		"prefix": opts.Prefix,
		"limit":  opts.Limit,
		"offset": opts.Offset,
		"sortBy": map[string]any{
			"column": opts.SortColumn,
			"order":  opts.SortOrder,
		},
	}
	if opts.Search != "" {
		payload["search"] = opts.Search
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("build list payload: %w", err)
	}

	listURL := fmt.Sprintf("%s/storage/v1/object/list/%s", strings.TrimRight(cfg.BaseURL, "/"), url.PathEscape(bucket))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, listURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("list request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list request status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if len(raw) == 0 {
		return []any{}, nil
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return data, nil
}

func (a *App) adminStorageDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdminToken(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := loadSupabaseStorageConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	bucketValue := firstNonEmpty(r.URL.Query().Get("bucket"), cfg.DefaultBucket)
	bucket, err := cleanStorageBucket(bucketValue)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	objectPath, err := cleanStoragePath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "query param path is required",
		})
		return
	}

	deleteURL := buildSupabaseObjectURL(cfg.BaseURL, bucket, objectPath)
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to build delete request",
		})
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage delete request failed",
		})
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage delete failed",
			"details": strings.TrimSpace(string(raw)),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"bucket": bucket,
			"path":   objectPath,
		},
	})
}

type adminAuthConfig struct {
	StaticToken   string
	Username      string
	Password      string
	SigningSecret string
	SessionTTL    time.Duration
}

type adminAccessTokenClaims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Jti      string `json:"jti"`
}

func requireAdminToken(w http.ResponseWriter, r *http.Request) bool {
	cfg, err := loadAdminAuthConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return false
	}

	provided := extractAdminToken(r)
	if provided == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"status":  "error",
			"message": "missing admin token",
		})
		return false
	}

	if cfg.StaticToken != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.StaticToken)) == 1 {
		return true
	}

	if cfg.SigningSecret != "" {
		if _, err := verifyAdminAccessToken(provided, cfg.SigningSecret, time.Now()); err == nil {
			return true
		}
	}

	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"status":  "error",
		"message": "unauthorized",
	})
	return false
}

func extractAdminToken(r *http.Request) string {
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if provided != "" {
		return provided
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}

	return ""
}

func loadAdminAuthConfig() (adminAuthConfig, error) {
	cfg := adminAuthConfig{
		StaticToken:   strings.TrimSpace(os.Getenv("ADMIN_TOKEN")),
		Username:      strings.TrimSpace(os.Getenv("ADMIN_USERNAME")),
		Password:      strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")),
		SigningSecret: strings.TrimSpace(firstNonEmpty(os.Getenv("ADMIN_JWT_SECRET"), os.Getenv("JWT_SECRET"))),
		SessionTTL:    resolveAdminSessionTTL(),
	}

	hasStaticToken := cfg.StaticToken != ""
	hasCredentials := cfg.Username != "" && cfg.Password != ""
	if !hasStaticToken && !hasCredentials {
		return adminAuthConfig{}, errors.New("admin auth is not configured (set ADMIN_TOKEN or ADMIN_USERNAME and ADMIN_PASSWORD)")
	}
	if hasCredentials && cfg.SigningSecret == "" && !hasStaticToken {
		return adminAuthConfig{}, errors.New("JWT_SECRET or ADMIN_JWT_SECRET is required when ADMIN_USERNAME and ADMIN_PASSWORD are set")
	}

	return cfg, nil
}

func resolveAdminSessionTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("ADMIN_SESSION_TTL"))
	if raw == "" {
		return defaultAdminSession
	}

	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return defaultAdminSession
	}
	if ttl > maxAdminSession {
		return maxAdminSession
	}
	return ttl
}

func issueAdminAccessToken(username, signingSecret string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	if strings.TrimSpace(signingSecret) == "" {
		return "", time.Time{}, errors.New("signing secret is required")
	}
	if ttl <= 0 {
		ttl = defaultAdminSession
	}

	jtiRaw := make([]byte, 16)
	if _, err := rand.Read(jtiRaw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token id: %w", err)
	}

	expiresAt := now.Add(ttl).UTC()
	claims := adminAccessTokenClaims{
		Sub:      "admin",
		Username: strings.TrimSpace(username),
		Iat:      now.UTC().Unix(),
		Exp:      expiresAt.Unix(),
		Jti:      base64.RawURLEncoding.EncodeToString(jtiRaw),
	}

	rawClaims, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal token claims: %w", err)
	}

	payload := base64.RawURLEncoding.EncodeToString(rawClaims)
	signature := signAdminTokenPayload(payload, signingSecret)
	token := adminTokenPrefix + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)

	return token, expiresAt, nil
}

func verifyAdminAccessToken(token, signingSecret string, now time.Time) (adminAccessTokenClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != adminTokenPrefix {
		return adminAccessTokenClaims{}, errors.New("invalid token format")
	}

	payload := parts[1]
	signatureRaw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return adminAccessTokenClaims{}, errors.New("invalid token signature")
	}

	expectedSignature := signAdminTokenPayload(payload, signingSecret)
	if subtle.ConstantTimeCompare(signatureRaw, expectedSignature) != 1 {
		return adminAccessTokenClaims{}, errors.New("invalid token signature")
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return adminAccessTokenClaims{}, errors.New("invalid token payload")
	}

	var claims adminAccessTokenClaims
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		return adminAccessTokenClaims{}, errors.New("invalid token claims")
	}

	if claims.Sub != "admin" {
		return adminAccessTokenClaims{}, errors.New("invalid token subject")
	}
	if claims.Exp <= now.UTC().Unix() {
		return adminAccessTokenClaims{}, errors.New("token expired")
	}

	return claims, nil
}

func signAdminTokenPayload(payload, signingSecret string) []byte {
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func loadSupabaseStorageConfig() (supabaseStorageConfig, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	serviceRole := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
	defaultBucket := strings.TrimSpace(os.Getenv("STORAGE_BUCKET"))
	if defaultBucket == "" {
		defaultBucket = "cars"
	}

	if baseURL == "" {
		return supabaseStorageConfig{}, errors.New("SUPABASE_URL is not set")
	}
	if serviceRole == "" {
		return supabaseStorageConfig{}, errors.New("SUPABASE_SERVICE_ROLE_KEY is not set")
	}

	return supabaseStorageConfig{
		BaseURL:       baseURL,
		ServiceRole:   serviceRole,
		DefaultBucket: defaultBucket,
	}, nil
}

func cleanStorageBucket(value string) (string, error) {
	bucket := strings.TrimSpace(value)
	if bucket == "" {
		return "", errors.New("bucket is required")
	}
	if strings.ContainsAny(bucket, `/\`) || strings.Contains(bucket, "..") {
		return "", errors.New("invalid bucket")
	}
	return bucket, nil
}

func cleanOptionalStoragePath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	return cleanStoragePath(trimmed)
}

func cleanStoragePath(value string) (string, error) {
	candidate := strings.TrimSpace(strings.ReplaceAll(value, `\`, `/`))
	candidate = strings.Trim(candidate, "/")
	if candidate == "" {
		return "", errors.New("path is required")
	}

	parts := strings.Split(candidate, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if clean == "" {
			continue
		}
		if clean == "." || clean == ".." {
			return "", errors.New("invalid path")
		}
		result = append(result, clean)
	}
	if len(result) == 0 {
		return "", errors.New("path is required")
	}

	return strings.Join(result, "/"), nil
}

func sanitizeFilename(value string) string {
	name := strings.TrimSpace(strings.ReplaceAll(value, `\`, `/`))
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "/")
	base := strings.TrimSpace(parts[len(parts)-1])
	base = strings.Trim(base, ".")
	if base == "" || base == "." || base == ".." {
		return ""
	}
	return base
}

func buildSupabaseObjectURL(baseURL, bucket, objectPath string) string {
	return fmt.Sprintf(
		"%s/storage/v1/object/%s/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(bucket),
		encodeStoragePath(objectPath),
	)
}

func buildSupabasePublicURL(baseURL, bucket, objectPath string) string {
	return fmt.Sprintf(
		"%s/storage/v1/object/public/%s/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(bucket),
		encodeStoragePath(objectPath),
	)
}

func encodeStoragePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func parseIntOrDefault(raw string, fallback int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return number, nil
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	var pingErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		pingErr = db.PingContext(ctx)
		cancel()
		if pingErr == nil {
			break
		}
		log.Printf("database ping attempt %d/3 failed: %v", attempt, pingErr)
		time.Sleep(2 * time.Second)
	}
	if pingErr != nil {
		db.Close()
		return nil, pingErr
	}
	return db, nil
}

func loadDotEnv() error {
	// Load silently if file is absent so containers can rely on injected env.
	if _, err := os.Stat(".env"); err != nil {
		return nil
	}
	return godotenv.Load()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullStringValue(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

func uniqueNonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result
}

func parsePerformedWorks(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}

	var asStrings []string
	if err := json.Unmarshal(raw, &asStrings); err == nil {
		return uniqueNonEmpty(asStrings...)
	}

	var asAny []any
	if err := json.Unmarshal(raw, &asAny); err != nil {
		return []string{}
	}

	works := make([]string, 0, len(asAny))
	for _, item := range asAny {
		switch v := item.(type) {
		case string:
			works = append(works, v)
		case map[string]any:
			for _, key := range []string{"step", "title", "name", "text", "description"} {
				val, ok := v[key]
				if !ok {
					continue
				}
				text, ok := val.(string)
				if ok && strings.TrimSpace(text) != "" {
					works = append(works, text)
					break
				}
			}
		}
	}

	return uniqueNonEmpty(works...)
}

func parseStringArray(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}

	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		result := make([]string, 0, len(items))
		for _, item := range items {
			clean := strings.TrimSpace(item)
			if clean == "" {
				continue
			}
			result = append(result, clean)
		}
		return result
	}

	// Handle case when JSONB contains a string with encoded JSON array.
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		var nested []string
		if err := json.Unmarshal([]byte(encoded), &nested); err == nil {
			result := make([]string, 0, len(nested))
			for _, item := range nested {
				clean := strings.TrimSpace(item)
				if clean == "" {
					continue
				}
				result = append(result, clean)
			}
			return result
		}
	}

	return []string{}
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func optionalStringDBValue(input string) any {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func optionalStringValue(input string) *string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeDSN(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}

	q := parsed.Query()
	if strings.TrimSpace(q.Get("connect_timeout")) == "" {
		q.Set("connect_timeout", "10")
	}
	if strings.TrimSpace(q.Get("sslmode")) == "" {
		q.Set("sslmode", "require")
	}
	if strings.TrimSpace(q.Get("default_query_exec_mode")) == "" {
		q.Set("default_query_exec_mode", "simple_protocol")
	}
	if strings.TrimSpace(q.Get("statement_cache_capacity")) == "" {
		q.Set("statement_cache_capacity", "0")
	}
	parsed.RawQuery = q.Encode()

	return parsed.String()
}

func validateConsultationRequest(firstName, lastName, phone, serviceType, carModel, preferredCallTime, comments string) map[string]string {
	errs := map[string]string{}

	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	phone = strings.TrimSpace(phone)
	serviceType = strings.TrimSpace(serviceType)
	carModel = strings.TrimSpace(carModel)
	preferredCallTime = strings.TrimSpace(preferredCallTime)
	comments = strings.TrimSpace(comments)

	if firstName == "" {
		errs["first_name"] = "???????? ??????????????????????"
	} else if len([]rune(firstName)) > 100 {
		errs["first_name"] = "???????????????? 100 ????????????????"
	}

	if lastName == "" {
		errs["last_name"] = "???????? ??????????????????????"
	} else if len([]rune(lastName)) > 100 {
		errs["last_name"] = "???????????????? 100 ????????????????"
	}

	if phone == "" {
		errs["phone"] = "???????? ??????????????????????"
	} else if !phonePattern.MatchString(phone) {
		errs["phone"] = "???????????????????????? ???????????? ????????????"
	}

	if serviceType == "" {
		errs["service_type"] = "???????? ??????????????????????"
	} else if len([]rune(serviceType)) > 80 {
		errs["service_type"] = "???????????????? 80 ????????????????"
	}

	if len([]rune(carModel)) > 120 {
		errs["car_model"] = "???????????????? 120 ????????????????"
	}
	if len([]rune(preferredCallTime)) > 120 {
		errs["preferred_call_time"] = "???????????????? 120 ????????????????"
	}
	if len([]rune(comments)) > 2000 {
		errs["comments"] = "???????????????? 2000 ????????????????"
	}

	return errs
}

type consultationNotification struct {
	ID                int64     `json:"id"`
	FirstName         string    `json:"first_name"`
	LastName          string    `json:"last_name"`
	Phone             string    `json:"phone"`
	ServiceType       string    `json:"service_type"`
	CarModel          *string   `json:"car_model"`
	PreferredCallTime *string   `json:"preferred_call_time"`
	Comments          *string   `json:"comments"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
}

func notifyAdminAboutConsultation(payload consultationNotification) {
	webhookURL := strings.TrimSpace(os.Getenv("ADMIN_NOTIFY_WEBHOOK_URL"))
	if webhookURL == "" {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("consultation notify marshal error: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("consultation notify request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		log.Printf("consultation notify send error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("consultation notify non-2xx status: %s", resp.Status)
	}
}

func resolveWorkPostTable(ctx context.Context, db *sql.DB) (string, error) {
	var workPost sql.NullString
	var blogPosts sql.NullString

	err := db.QueryRowContext(
		ctx,
		`SELECT to_regclass('public.work_post')::text, to_regclass('public.blog_posts')::text`,
	).Scan(&workPost, &blogPosts)
	if err != nil {
		return "", err
	}

	if workPost.Valid && strings.TrimSpace(workPost.String) != "" {
		return "work_post", nil
	}
	if blogPosts.Valid && strings.TrimSpace(blogPosts.String) != "" {
		return "blog_posts", nil
	}
	return "", nil
}

func resolveTuningTable(ctx context.Context, db *sql.DB) (string, error) {
	var tuning sql.NullString
	var tunning sql.NullString

	err := db.QueryRowContext(
		ctx,
		`SELECT to_regclass('public.tuning')::text, to_regclass('public.tunning')::text`,
	).Scan(&tuning, &tunning)
	if err != nil {
		return "", err
	}

	if tuning.Valid && strings.TrimSpace(tuning.String) != "" {
		return "tuning", nil
	}
	if tunning.Valid && strings.TrimSpace(tunning.String) != "" {
		return "tunning", nil
	}
	return "", nil
}

func hasColumn(ctx context.Context, db *sql.DB, tableName, columnName string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		)`,
		tableName,
		columnName,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func hasTable(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name = $1
		)`,
		tableName,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// loggingMiddleware adds a minimal request log.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
