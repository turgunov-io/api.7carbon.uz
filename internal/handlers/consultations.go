package handlers

// Consultation requests submitted by site visitors.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"carbon_go/internal/render"
)

var phonePattern = regexp.MustCompile(`^\+?[0-9]{7,15}$`)

func (a *App) consultationsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.createConsultationHandler(w, r)
	case http.MethodGet:
		a.listConsultationsHandler(w, r)
	default:
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
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
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "???????????????????????? JSON",
			"errors": map[string]string{
				"body": "?????????????????? ???????????? ??????????????",
			},
		})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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
		render.JSON(w, http.StatusUnprocessableEntity, map[string]any{
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
	carModel := render.OptionalStringDBValue(req.CarModel)
	preferredCallTime := render.OptionalStringDBValue(req.PreferredCallTime)
	comments := render.OptionalStringDBValue(req.Comments)

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
		render.JSON(w, http.StatusInternalServerError, map[string]any{
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
		CarModel:          render.OptionalStringValue(req.CarModel),
		PreferredCallTime: render.OptionalStringValue(req.PreferredCallTime),
		Comments:          render.OptionalStringValue(req.Comments),
		Status:            "new",
		CreatedAt:         createdAt,
	})

	render.JSON(w, http.StatusCreated, map[string]any{
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
		render.JSON(w, http.StatusInternalServerError, map[string]any{
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
			render.JSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "???? ?????????????? ?????????????????? ????????????",
			})
			return
		}

		item.CarModel = render.NullableString(carModel)
		item.PreferredCallTime = render.NullableString(preferredCallTime)
		item.Comments = render.NullableString(comments)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "???? ?????????????? ?????????????????? ????????????",
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data":   items,
	})
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

// cachedSchemaLookup memoizes an information_schema probe for the process
// lifetime. The DSN targets the Supabase transaction pooler with
// simple_protocol, so every probe is an uncached network round-trip; repeating
// them per request dominated latency on read endpoints. Only successful lookups
