// Package static встраивает статику интерфейса web: стили, собственный скрипт, эмблему и вендоренный htmx 2.
// Отдаётся самим сервисом с того же источника, поэтому CSP обходится без внешних CDN и без 'unsafe-inline'
// (ADR-0007, ADR-0018). README в vendor не встраивается: это документация, а не ресурс страницы.
package static

import "embed"

// FS — файлы статики с путями относительно каталога static: "css/app.css", "vendor/htmx.min.js" и т. д.
//
//go:embed css/*.css js/*.js img/*.svg vendor/*.js vendor/ext/*.js
var FS embed.FS
