# Сторонние скрипты интерфейса

Скрипты лежат в репозитории и отдаются самим `web` (ADR-0007): внешних CDN нет, CSP `script-src 'self'`.
Файлы не правятся вручную; обновление — заменой файла целиком и новой строкой в таблице.

| Файл | Пакет и версия | Источник | SHA-256 | Лицензия |
| --- | --- | --- | --- | --- |
| `htmx.min.js` | `htmx.org` 2.0.4 | `https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js` | `e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447` | 0BSD |
| `ext/sse.js` | `htmx-ext-sse` 2.2.2 | `https://unpkg.com/htmx-ext-sse@2.2.2/sse.js` | `83eca6fa0611fe2b0bf1700b424b88b5eced38ef448ef9760a2ea08fbc875611` | 0BSD |

Загружено 23.10.2026. Для `htmx.min.js` SRI `sha384-HGfztofotfshcF7+8n44JQL2oJmowVChPTg48S+jvZoztPfvwD79OC/LTtG6dMp+`
совпадает с опубликованным в документации htmx для версии 2.0.4.

Проверка после обновления:

```
sha256sum services/web/static/vendor/htmx.min.js services/web/static/vendor/ext/sse.js
```

Настройка htmx — тег `<meta name="htmx-config">` в макете страницы (`services/web/internal/ui/templates/layout.html`):
`includeIndicatorStyles: false` (иначе htmx вставит `<style>`, который CSP запретит), `allowEval: false`,
`allowScriptTags: false`. Атрибуты `hx-on*` в шаблонах запрещены тестом: это встроенный обработчик.
