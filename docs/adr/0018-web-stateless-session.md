# ADR-0018. Сервис web без базы: сессия в cookie и защита форм

Статус: принят, 23.10.2026.

## Контекст

`web` — BFF и интерфейс (ADR-0007): вход через Keycloak по Authorization Code + PKCE (ADR-0006), передача JWT
пользователя в сервисы, аудит входа, выхода и отказов по HTTP (ADR-0017). Собственных данных предметной области у него
нет. Чек-лист этапа 2 называл для сессии `gorilla/securecookie`; модуля нет в кэше модулей, а сеть на машине разработки
недоступна, и задача решается стандартной библиотекой. Каркас `libs/runtime` (ADR-0015) до сих пор требовал базу и
проверял bearer JWT у каждого запроса, что для браузерного сервиса не подходит.

## Решение

**База.** У `web` нет PostgreSQL: нет пула, миграций, справочников в базе и ретранслятора outbox. В `libs/runtime`
появляется режим `Spec.NoDatabase`. Сессия живёт в cookie, кэш (если понадобится) — в памяти процесса. Так `web`
масштабируется горизонтально без общего хранилища, а единственный источник истины о пользователе — Keycloak.

**Вход.** В `libs/runtime` появляется `Spec.CustomAuth`: сервис сам ставит свою цепочку вместо проверки bearer JWT
(`Runtime.Use`). У `web` это проверка сессии, CSRF и заголовки безопасности. Без цепочки при `CustomAuth` запуск
останавливается: доступ без проверки не открывается по ошибке конфигурации.

**Сессия.**
- Значение cookie — JSON с полями `exp` и `iat`, зашифрованный AES-256-GCM (`crypto/aes`, `crypto/cipher`) со
  случайным nonce; имя cookie — дополнительные данные AEAD, поэтому зашифрованное значение одной cookie нельзя
  подставить в другую. Подделанная, чужим ключом зашифрованная или просроченная cookie отбрасывается, пользователь
  идёт на вход.
- Ключ — 32 байта в base64 из `BS_SESSION_KEY`. Ротация ключа: `BS_SESSION_KEY_PREVIOUS` только расшифровывает,
  новые cookie всегда шифруются текущим ключом. В `.env.example` — демонстрационный ключ.
- Срок жизни сессии `BS_SESSION_TTL` (по умолчанию 8 ч, как простой SSO-сессии realm). Cookie перевыпускается при
  входе (новый CSRF-токен, защита от фиксации сессии) и при каждом обновлении access token по refresh token.
- Access token Keycloak и refresh token вместе с шифрованием не помещаются в одну cookie (лимит браузера около 4 КБ),
  поэтому значение режется на части `bs_session.0`, `bs_session.1`, … (не больше восьми); лишние части прежней версии
  удаляются при записи.
- После входа `id_token` не хранится: он нужен только для проверки `nonce` в callback. Выход идёт на end-session
  Keycloak с `client_id` и `post_logout_redirect_uri` без `id_token_hint`.
- Состояние входа (`state`, `nonce`, PKCE verifier, адрес возврата) — в отдельной cookie `bs_auth` на 10 минут.
- Атрибуты cookie: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` (`BS_COOKIE_SECURE`, по умолчанию включено).

**Права.** Роли и `clinic_id` берутся из access token, проверенного по JWKS Keycloak той же библиотекой
`libs/httpx/auth`, что и в сервисах; матрица прав одна (`policy.go`). Отказ — страница 403 по-русски и запись
`ACCESS_DENIED` с результатом `DENIED` в аудит через `POST /records` служебным токеном (ADR-0017).

**Формы.** Каждый POST (и любой небезопасный метод) несёт CSRF-токен сессии в поле `csrf_token` или в заголовке
`X-CSRF-Token` (HTMX). Сравнение — за постоянное время. Нет токена или он не совпал — 403. Выход — только POST.

**Заголовки на всех ответах.**
- `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none';
  form-action 'self' <origin Keycloak>` — без `unsafe-inline`: ни встроенных скриптов, ни атрибутов `style`, ни
  обработчиков `on*` в шаблонах. Источник Keycloak в `form-action` нужен, потому что форма выхода перенаправляет на
  end-session.
- `Strict-Transport-Security: max-age=31536000` — только если запрос пришёл по TLS или через nginx
  (`X-Forwarded-Proto: https`); `BS_HSTS_MAX_AGE=0` выключает.
- `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`,
  `Cross-Origin-Opener-Policy: same-origin`, `Cache-Control: no-store` для страниц с сессией.

**Шапка.** В интерфейсе показывается только логин (`preferred_username`), роль и клиника. ФИО не запрашивается и не
выводится (CONTRIBUTING.md: в системе нет ФИО); scope `profile` нужен Keycloak только для `preferred_username`.

## Альтернативы

- `gorilla/securecookie`: та же схема (AES + HMAC), но лишняя зависимость; AES-GCM даёт шифрование и целостность одним примитивом.
- Сессии на сервере (PostgreSQL или Redis): отзыв сессии мгновенный, но появляется база у BFF или новая технология
  вне раздела 5 плана. Отзыв в нашей схеме ограничен сроком access token (15 мин) — refresh у отозванной SSO-сессии
  Keycloak не пройдёт.
- Хранить `id_token` ради `id_token_hint`: Keycloak тогда выходит без страницы подтверждения, но cookie растёт ещё на
  1–2 КБ. Страница подтверждения выхода в демо допустима.

## Последствия

- Шаблоны UI пишутся без встроенных скриптов и стилей; HTMX настраивается тегом `meta` с
  `includeIndicatorStyles: false`, иначе он вставит `<style>`, который CSP заблокирует.
- Смена `BS_SESSION_KEY` без `BS_SESSION_KEY_PREVIOUS` разлогинивает всех — это и есть аварийный отзыв всех сессий.
- `web` не может сам отозвать конкретную сессию до истечения access token: окно — `accessTokenLifespan` realm.
- `libs/runtime` обслуживает сервисы и с базой, и без неё; поведение сервисов с базой не меняется.

## Условия пересмотра

Требование мгновенного отзыва сессии пользователя или рост cookie сверх восьми частей — серверное хранилище сессий
отдельным ADR.
