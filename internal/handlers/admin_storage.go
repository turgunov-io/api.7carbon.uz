package handlers

// Admin endpoints backed by Supabase Storage.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"carbon_go/internal/auth"
	"carbon_go/internal/env"
	"carbon_go/internal/render"
	"carbon_go/internal/storage"
)

func (a *App) adminStorageUploadHandler(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireToken(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := storage.LoadConfig()
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	maxBytes := int64(storageUploadMaxMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "file is required (form-data key: file)",
		})
		return
	}
	defer file.Close()

	bucketValue := env.FirstNonEmpty(r.FormValue("bucket"), cfg.DefaultBucket)
	bucket, err := storage.CleanBucket(bucketValue)
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	filename := storage.SanitizeFilename(env.FirstNonEmpty(r.FormValue("filename"), fileHeader.Filename))
	if filename == "" {
		filename = fmt.Sprintf("upload_%d.bin", time.Now().UnixNano())
	}

	folder, err := storage.CleanOptionalPath(r.FormValue("folder"))
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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

	uploadURL := storage.ObjectURL(cfg.BaseURL, bucket, objectPath)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, file)
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
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
		render.JSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage upload request failed",
		})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		render.JSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage upload failed",
			"details": strings.TrimSpace(string(respBody)),
		})
		return
	}

	render.JSON(w, http.StatusCreated, map[string]any{
		"status": "success",
		"data": map[string]any{
			"bucket":        bucket,
			"path":          objectPath,
			"mime_type":     contentType,
			"size":          fileHeader.Size,
			"upsert":        upsert,
			"storage_url":   storage.ObjectURL(cfg.BaseURL, bucket, objectPath),
			"public_url":    storage.PublicURL(cfg.BaseURL, bucket, objectPath),
			"storage_reply": strings.TrimSpace(string(respBody)),
		},
	})
}

func (a *App) adminStorageListHandler(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireToken(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := storage.LoadConfig()
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	prefix, err := storage.CleanOptionalPath(r.URL.Query().Get("prefix"))
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	limit, err := env.ParseIntOrDefault(r.URL.Query().Get("limit"), defaultStorageLimit)
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
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

	offset, err := env.ParseIntOrDefault(r.URL.Query().Get("offset"), 0)
	if err != nil || offset < 0 {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "offset must be a non-negative integer",
		})
		return
	}

	sortColumn := env.FirstNonEmpty(strings.TrimSpace(r.URL.Query().Get("sort_column")), "name")
	sortOrder := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort_order")))
	if sortOrder != "desc" {
		sortOrder = "asc"
	}

	opts := storage.ListOptions{
		Prefix:     prefix,
		Limit:      limit,
		Offset:     offset,
		SortColumn: sortColumn,
		SortOrder:  sortOrder,
		Search:     strings.TrimSpace(r.URL.Query().Get("search")),
	}

	allBuckets := env.IsTruthy(r.URL.Query().Get("all"))
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	if allBuckets {
		buckets, err := storage.ListBuckets(ctx, cfg)
		if err != nil {
			render.JSON(w, http.StatusBadGateway, map[string]any{
				"status":  "error",
				"message": "failed to fetch buckets",
				"details": err.Error(),
			})
			return
		}

		items := make(map[string]any, len(buckets))
		for _, bucket := range buckets {
			data, err := storage.ListBucketFiles(ctx, cfg, bucket, opts)
			if err != nil {
				items[bucket] = map[string]any{
					"status":  "error",
					"message": err.Error(),
				}
				continue
			}
			items[bucket] = data
		}

		render.JSON(w, http.StatusOK, map[string]any{
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

	bucketValue := env.FirstNonEmpty(r.URL.Query().Get("bucket"), cfg.DefaultBucket)
	bucket, err := storage.CleanBucket(bucketValue)
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	data, err := storage.ListBucketFiles(ctx, cfg, bucket, opts)
	if err != nil {
		render.JSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage list failed",
			"details": err.Error(),
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
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
func (a *App) adminStorageDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireToken(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		render.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"status":  "error",
			"message": "method not allowed",
		})
		return
	}

	cfg, err := storage.LoadConfig()
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	bucketValue := env.FirstNonEmpty(r.URL.Query().Get("bucket"), cfg.DefaultBucket)
	bucket, err := storage.CleanBucket(bucketValue)
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	objectPath, err := storage.CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		render.JSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "query param path is required",
		})
		return
	}

	deleteURL := storage.ObjectURL(cfg.BaseURL, bucket, objectPath)
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		render.JSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "failed to build delete request",
		})
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		render.JSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage delete request failed",
		})
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		render.JSON(w, http.StatusBadGateway, map[string]any{
			"status":  "error",
			"message": "storage delete failed",
			"details": strings.TrimSpace(string(raw)),
		})
		return
	}

	render.JSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"data": map[string]any{
			"bucket": bucket,
			"path":   objectPath,
		},
	})
}
