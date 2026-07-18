package handlers

// Generic table-backed CRUD used by every admin resource route.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"carbon_go/internal/auth"
	"carbon_go/internal/database"
	"carbon_go/internal/render"
)

var adminOrderClausePattern = regexp.MustCompile(`^t\.([a-zA-Z_][a-zA-Z0-9_]*)\s+(ASC|DESC)$`)
var loggedAdminOrderFallbacks sync.Map

// contactsAPIConfig backs the public /api/contacts CRUD routes.
var contactsAPIConfig = tableCRUDConfig{
	Path:             "/api/contacts",
	Table:            "public.contact",
	OrderBy:          "t.id ASC",
	MutableColumns:   columnSet("phone_number", "address", "description", "email", "work_schedule"),
	RequiredOnCreate: columnSet(),
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
			Path:             "/admin/accessories",
			Table:            "public.accessories",
			OrderBy:          "t.created_at DESC, t.id DESC",
			MutableColumns:   columnSet("title", "card_image_url", "full_image_url", "price", "description"),
			RequiredOnCreate: columnSet("title"),
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
		if !auth.RequireToken(w, r) {
			return
		}

		id, hasID, err := parseResourceID(r, cfg.Path)
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
				a.adminFetchOne(w, r, cfg, id)
				return
			}
			a.adminFetchMany(w, r, cfg)
		case http.MethodPost:
			a.adminCreateOne(w, r, cfg)
		case http.MethodPut, http.MethodPatch:
			if !hasID {
				render.JSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": "id is required",
				})
				return
			}
			a.adminUpdateOne(w, r, cfg, id)
		case http.MethodDelete:
			if !hasID {
				render.JSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": "id is required",
				})
				return
			}
			a.adminDeleteOne(w, r, cfg, id)
		default:
			render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
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
				render.JSON(w, http.StatusInternalServerError, map[string]any{
					"status":  "error",
					"message": "failed to fetch data",
				})
				return
			}
		} else {
			log.Printf("admin list %s failed: %v", cfg.Table, err)
			render.JSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to fetch data",
			})
			return
		}
	}

	// Postgres already returned valid JSON, so hand it through untouched rather
	// than paying an Unmarshal into `any` plus a re-Marshal on every list.
	data := json.RawMessage(raw)
	if len(raw) == 0 {
		data = json.RawMessage("[]")
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) resolveAdminListOrderBy(ctx context.Context, tableName, desired string) (string, error) {
	orderBy := strings.TrimSpace(desired)
	if orderBy == "" {
		return "t.id ASC", nil
	}

	columns, err := database.TableColumns(ctx, a.DB, tableName)
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

// querySchemaVariant runs the first query in a compatibility ladder that the
// live schema accepts. The winning index is cached per key, so later requests go
// straight to it instead of re-paying a failed round-trip for every earlier
// variant. If the cached variant ever stops working the ladder is retried from
func (a *App) queryAdminList(ctx context.Context, tableName, orderBy string, out *[]byte) error {
	query := fmt.Sprintf(
		`SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY %s), '[]'::json) FROM %s t`,
		orderBy,
		database.QuoteTableName(tableName),
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

	query := fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE t.id = $1`, database.QuoteTableName(cfg.Table))

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			render.JSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin fetch one %s failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to fetch data",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin fetch one %s decode failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse data",
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
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
		render.JSON(w, http.StatusUnprocessableEntity, map[string]any{
			"status":  "error",
			"message": "validation error",
			"errors":  validationErrors,
		})
		return
	}
	if cfg.Path == "/admin/tuning" {
		if err := a.alignTuningPayloadToSchema(ctx, payload); err != nil {
			log.Printf("admin create %s schema alignment failed: %v", cfg.Table, err)
			render.JSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to prepare payload",
			})
			return
		}
	}

	keys := sortedMapKeys(payload)
	quotedTable := database.QuoteTableName(cfg.Table)

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
			columns = append(columns, database.QuoteIdentifier(key))
			placeholders = append(placeholders, fmt.Sprintf("$%d", idx+1))

			value, err := normalizeCRUDValue(key, payload[key], cfg)
			if err != nil {
				render.JSON(w, http.StatusBadRequest, map[string]any{
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
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to create record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin create %s decode failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse created record",
		})
		return
	}

	render.JSON(w, http.StatusCreated, map[string]any{
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
		render.JSON(w, http.StatusUnprocessableEntity, map[string]any{
			"status":  "error",
			"message": "validation error",
			"errors":  validationErrors,
		})
		return
	}
	if cfg.Path == "/admin/tuning" {
		if err := a.alignTuningPayloadToSchema(ctx, payload); err != nil {
			log.Printf("admin update %s schema alignment failed: %v", cfg.Table, err)
			render.JSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "failed to prepare payload",
			})
			return
		}
	}

	keys := sortedMapKeys(payload)
	if len(keys) == 0 && !cfg.TouchUpdatedAt {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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
			render.JSON(w, http.StatusBadRequest, map[string]any{
				"status":  "error",
				"message": err.Error(),
			})
			return
		}
		args = append(args, value)
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", database.QuoteIdentifier(key), idx+1))
	}
	if cfg.TouchUpdatedAt {
		setClauses = append(setClauses, `updated_at = NOW()`)
	}

	query := fmt.Sprintf(
		`WITH upd AS (UPDATE %s SET %s WHERE id = $%d RETURNING *) SELECT to_jsonb(upd) FROM upd`,
		database.QuoteTableName(cfg.Table),
		strings.Join(setClauses, ", "),
		len(args)+1,
	)
	args = append(args, id)

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			render.JSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin update %s failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to update record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin update %s decode failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse updated record",
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   data,
	})
}

func (a *App) adminDeleteOne(w http.ResponseWriter, r *http.Request, cfg tableCRUDConfig, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	query := fmt.Sprintf(
		`WITH del AS (DELETE FROM %s WHERE id = $1 RETURNING *) SELECT to_jsonb(del) FROM del`,
		database.QuoteTableName(cfg.Table),
	)

	var raw []byte
	if err := a.DB.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			render.JSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": "record not found",
			})
			return
		}
		log.Printf("admin delete %s failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to delete record",
		})
		return
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		log.Printf("admin delete %s decode failed: %v", cfg.Table, err)
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to parse deleted record",
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
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
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "invalid JSON body",
		})
		return nil, false
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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
	hasTitleColumn, err := database.HasColumn(ctx, a.DB, "tuning", "title")
	if err != nil {
		return err
	}
	hasDescriptionColumn, err := database.HasColumn(ctx, a.DB, "tuning", "description")
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
func logAdminOrderFallbackOnce(tableName, clause, columnName string) {
	key := tableName + "|" + columnName + "|" + clause
	if _, loaded := loggedAdminOrderFallbacks.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("admin list %s: dropping order clause %q because column %s does not exist", tableName, clause, columnName)
}
