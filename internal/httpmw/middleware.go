// Package httpmw holds the HTTP middleware chain: request logging, CORS, and
// the response layer that adds conditional-GET and compression.
package httpmw

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Logging adds a minimal request log.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}

// CORS allows browser clients from any origin to call the API.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token, If-None-Match")
		// Browsers cannot read ETag cross-origin unless it is explicitly exposed,
		// and without it they will never send If-None-Match back.
		w.Header().Set("Access-Control-Expose-Headers", "ETag")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// bufferingResponseWriter captures a handler's response so the middleware can
// hash it for an ETag and compress it before anything reaches the socket.
type bufferingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	buf         bytes.Buffer
}

func (w *bufferingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.buf.Write(b)
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gz
	},
}

// gzipMinBytes is the payload size below which compression costs more than it
// saves once framing overhead is counted.
const gzipMinBytes = 1024

// Response adds conditional-GET (ETag/304) and gzip to every JSON response.
// Payloads here are highly repetitive JSON that changes rarely, so both cut a
// lot of bytes off the wire without touching handler code.
func Response(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &bufferingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		body := rec.buf.Bytes()
		header := w.Header()
		isRead := r.Method == http.MethodGet || r.Method == http.MethodHead

		if isRead && rec.status == http.StatusOK && len(body) > 0 {
			sum := sha256.Sum256(body)
			etag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
			header.Set("ETag", etag)

			if matchesETag(r.Header.Get("If-None-Match"), etag) {
				header.Del("Content-Type")
				header.Del("Content-Length")
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		if len(body) >= gzipMinBytes &&
			header.Get("Content-Encoding") == "" &&
			strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {

			var compressed bytes.Buffer
			gz := gzipWriterPool.Get().(*gzip.Writer)
			gz.Reset(&compressed)
			_, writeErr := gz.Write(body)
			closeErr := gz.Close()
			gzipWriterPool.Put(gz)

			// Only adopt the compressed form if it actually helped; otherwise
			// fall through and send the original bytes.
			if writeErr == nil && closeErr == nil && compressed.Len() < len(body) {
				body = compressed.Bytes()
				header.Set("Content-Encoding", "gzip")
				header.Add("Vary", "Accept-Encoding")
			}
		}

		header.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(rec.status)
		if r.Method != http.MethodHead && len(body) > 0 {
			if _, err := w.Write(body); err != nil {
				log.Printf("response write failed for %s %s: %v", r.Method, r.URL.Path, err)
			}
		}
	})
}

// matchesETag reports whether an If-None-Match header covers the ETag.
func matchesETag(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if strings.TrimSpace(ifNoneMatch) == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
