package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/AlchemistBro/blood-supply-pet/services/web/static"
)

// staticTypes — типы содержимого задаются явно: на Windows реестр может сопоставить .js с text/plain,
// а при X-Content-Type-Options: nosniff браузер такой скрипт не выполнит.
var staticTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

// StaticHandler отдаёт встроенную статику; монтируется под StaticPrefix через http.StripPrefix.
// Каталоги не перечисляются. Совпала метка версии — кэш на год (immutable), иначе — на 5 минут.
// Путь с сегментом ".." отклоняется 404 сразу, даже если после очистки он ведёт к существующему файлу:
// ServeMux такой адрес и так перенаправляет на очищенный, а сюда он попадает только в обход маршрутизатора.
func (r *Renderer) StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw := strings.TrimPrefix(req.URL.Path, "/")
		if slices.Contains(strings.Split(raw, "/"), "..") {
			http.NotFound(w, req)
			return
		}
		name := path.Clean(raw)
		ctype, known := staticTypes[path.Ext(name)]
		if !known || !fs.ValidPath(name) {
			http.NotFound(w, req)
			return
		}
		st, err := fs.Stat(static.FS, name)
		if err != nil || st.IsDir() {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", ctype)
		if req.URL.Query().Get("v") == r.version {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		// G703: name прошёл fs.ValidPath, белый список расширений и fs.Stat во встроенной FS — за её пределы не выйти.
		http.ServeFileFS(w, req, static.FS, name) //nolint:gosec // G703: путь проверен выше, FS встроенная
	})
}

// contentVersion — 12 знаков SHA-256 от путей и содержимого всей статики.
func contentVersion() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(static.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static.FS, p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ui: версия статики: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}
