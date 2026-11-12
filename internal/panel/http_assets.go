package panel

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func responseETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}
func matchesETag(header, etag string) bool {
	for _, value := range strings.Split(header, ",") {
		value = strings.TrimSpace(value)
		if value == "*" || strings.TrimPrefix(value, "W/") == etag {
			return true
		}
	}
	return false
}

// Sensitive configuration endpoints intentionally keep using reply(), never a
// conditional response. These two snapshots are cached only in browser memory.
func replySnapshot(w http.ResponseWriter, r *http.Request, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		fail(w, 500, "No se pudo preparar la respuesta")
		return
	}
	tag := responseETag(data)
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json")
	if matchesETag(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

type publicAsset struct {
	once              sync.Once
	raw, compressed   []byte
	etag, contentType string
	err               error
}

// Cache only names present in the embedded asset manifest. Unknown URL paths
// cannot allocate cache entries. Public assets can be compressed; API secrets
// are never compressed or stored in this cache.
func publicFiles(files fs.FS) http.Handler {
	fallback := http.FileServer(http.FS(files))
	assets := map[string]*publicAsset{}
	versioned := regexp.MustCompile(`-[A-Za-z0-9_-]{6,}\.(js|css)$`)
	fs.WalkDir(files, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && versioned.MatchString(name) {
			assets["/"+name] = &publicAsset{}
		}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset, exists := assets[r.URL.Path]
		if !exists {
			w.Header().Set("Cache-Control", "no-cache")
			fallback.ServeHTTP(w, r)
			return
		}
		asset.once.Do(func() {
			asset.raw, asset.err = fs.ReadFile(files, strings.TrimPrefix(r.URL.Path, "/"))
			if asset.err != nil {
				return
			}
			asset.etag = responseETag(asset.raw)
			asset.contentType = mime.TypeByExtension(path.Ext(r.URL.Path))
			var out bytes.Buffer
			writer := gzip.NewWriter(&out)
			_, err := writer.Write(asset.raw)
			closeErr := writer.Close()
			if err == nil && closeErr == nil && out.Len() < len(asset.raw) {
				asset.compressed = out.Bytes()
			}
		})
		if asset.err != nil {
			http.NotFound(w, r)
			return
		}
		data, tag := asset.raw, asset.etag
		w.Header().Set("Vary", "Accept-Encoding")
		if len(asset.compressed) > 0 && acceptsGzip(r.Header.Get("Accept-Encoding")) {
			data = asset.compressed
			tag = strings.TrimSuffix(tag, `"`) + `-gzip"`
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Type", asset.contentType)
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(data))
	})
}
func acceptsGzip(header string) bool {
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ";")
		if !strings.EqualFold(parts[0], "gzip") {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(k, "q") {
				parsed, err := strconv.ParseFloat(v, 64)
				if err != nil || parsed < 0 || parsed > 1 {
					return false
				}
				quality = parsed
			}
		}
		return quality > 0
	}
	return false
}
