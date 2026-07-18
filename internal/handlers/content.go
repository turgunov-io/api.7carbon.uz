package handlers

// Public, read-only endpoints that serve site content.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"carbon_go/internal/auth"
	"carbon_go/internal/database"
	"carbon_go/internal/env"
	"carbon_go/internal/render"
)

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

	rows, err := database.QuerySchemaVariant(ctx, a.DB, "banners", queries)
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

		c.PhoneNumber = render.NullableString(phoneNumber)
		c.Address = render.NullableString(address)
		c.Description = render.NullableString(description)
		c.Email = render.NullableString(email)
		c.WorkSchedule = render.NullableString(workSchedule)

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
		render.JSON(w, http.StatusBadRequest, map[string]any{
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
		if !auth.RequireToken(w, r) {
			return
		}
		a.adminCreateOne(w, r, contactsAPIConfig)
	case http.MethodPut, http.MethodPatch:
		if !auth.RequireToken(w, r) {
			return
		}
		if !hasID {
			render.JSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": "id is required",
			})
			return
		}
		a.adminUpdateOne(w, r, contactsAPIConfig, id)
	case http.MethodDelete:
		if !auth.RequireToken(w, r) {
			return
		}
		if !hasID {
			render.JSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": "id is required",
			})
			return
		}
		a.adminDeleteOne(w, r, contactsAPIConfig, id)
	default:
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
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

	hasAboutPage, err := database.HasTable(ctx, a.DB, "about_page")
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
			pageItem.Title = render.NullableString(title)
			pageItem.BannerImageURL = render.NullableString(bannerImageURL)
			pageItem.IntroDescription = render.NullableString(introDescription)
			pageItem.MissionDescription = render.NullableString(missionDescription)
			pageItem.VideoURL = render.NullableString(videoURL)
			pageItem.MissionImageURL = render.NullableString(missionImageURL)
			page = &pageItem
		}
	}

	aboutID := 1
	if page != nil {
		aboutID = page.ID
	}

	hasAboutMetrics, err := database.HasTable(ctx, a.DB, "about_metrics")
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

	hasAboutSections, err := database.HasTable(ctx, a.DB, "about_sections")
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

	render.JSON(w, http.StatusOK, aboutResponse{
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

	rows, err := database.QuerySchemaVariant(ctx, a.DB, "tuning", queries)
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

		item.Brand = render.NullableString(brand)
		item.Model = render.NullableString(model)
		item.Title = render.NullableString(title)
		item.CardImageURL = render.NullableString(cardImageURL)
		item.FullImageURL = render.StringArray(fullImageURLRaw)
		item.Price = render.NullableString(price)
		item.Description = render.NullableString(description)
		item.CardDescription = render.NullableString(cardDescription)
		item.FullDescription = render.NullableString(fullDescription)
		item.VideoImageURL = render.NullableString(videoImageURL)
		item.VideoLink = render.NullableString(videoLink)

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

func (a *App) accessoriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	rows, err := a.DB.QueryContext(
		ctx,
		`SELECT id, title, card_image_url, full_image_url, price, description, created_at, updated_at
		FROM public.accessories
		ORDER BY created_at DESC, id DESC`,
	)
	if err != nil {
		http.Error(w, "failed to fetch accessories", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type accessoryItem struct {
		ID           int       `json:"id"`
		Title        string    `json:"title"`
		CardImageURL *string   `json:"card_image_url"`
		FullImageURL []string  `json:"full_image_url"`
		Price        *string   `json:"price"`
		Description  *string   `json:"description"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
	}

	items := make([]accessoryItem, 0, 8)
	for rows.Next() {
		var item accessoryItem
		var cardImageURL sql.NullString
		var fullImageURLRaw []byte
		var price sql.NullString
		var description sql.NullString

		if err := rows.Scan(
			&item.ID,
			&item.Title,
			&cardImageURL,
			&fullImageURLRaw,
			&price,
			&description,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			http.Error(w, "failed to read accessories", http.StatusInternalServerError)
			return
		}

		item.CardImageURL = render.NullableString(cardImageURL)
		item.FullImageURL = render.StringArray(fullImageURLRaw)
		item.Price = render.NullableString(price)
		item.Description = render.NullableString(description)

		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read accessories", http.StatusInternalServerError)
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

		item.Brand = render.NullableString(brand)
		item.Description = render.NullableString(description)
		item.YoutubeLink = render.NullableString(youtubeLink)

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

	tableName, err := database.ResolveWorkPostTable(ctx, a.DB)
	if err != nil {
		http.Error(w, "failed to resolve work posts table", http.StatusInternalServerError)
		return
	}
	if tableName == "" {
		http.Error(w, "work posts table not found", http.StatusInternalServerError)
		return
	}

	hasGalleryImages, err := database.HasColumn(ctx, a.DB, tableName, "gallery_images")
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

		cardURL := render.NullStringValue(cardImageURL)
		fullURL := render.NullStringValue(fullImageURL)
		videoImage := render.NullStringValue(videoImageURL)

		post.Title = titleModel
		post.Description = render.NullStringValue(cardDescription)
		post.FullDescription = render.NullStringValue(fullDescription)
		post.ImageURL = env.FirstNonEmpty(cardURL, fullURL, videoImage)
		post.VideoURL = render.NullStringValue(videoLink)
		post.PerformedWorks = render.PerformedWorks(workList)
		post.GalleryImages = render.StringArray(galleryImagesRaw)
		if len(post.GalleryImages) == 0 {
			post.GalleryImages = render.UniqueNonEmpty(cardURL, fullURL, videoImage)
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

	rows, err := database.QuerySchemaVariant(ctx, a.DB, "service_offerings", queries)
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

		item.ServiceType = render.NullableString(serviceType)
		item.Title = render.NullableString(title)
		item.DetailedDescription = render.NullableString(detailedDescription)
		item.GalleryImages = render.StringArray(galleryImagesRaw)
		item.PriceText = render.NullableString(priceText)

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
