# Матрица прав: роль × эндпоинт

<!-- Файл генерируется: go run ./tools/authmatrix -out docs/architecture/auth-matrix.md. Вручную не править. -->

Источник: расширение `x-bs-operation` операций в `services/*/api/openapi.yaml` и матрица `libs/httpx/auth/policy.go` (NFR011).
`2xx` — роль допущена к операции, `403` — отказ. Соглашение и проверки — `docs/architecture/testing.md`, раздел «Матрица прав».

## audit

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /internal/verify` | READ_AUDIT | 403 | 2xx | 2xx | 403 | 403 | 403 |
| `GET /records` | READ_AUDIT | 403 | 2xx | 2xx | 403 | 403 | 403 |
| `POST /records` | WRITE_AUDIT | 403 | 403 | 403 | 403 | 2xx | 403 |

## demand

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /files` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `POST /files` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `GET /files/{id}` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `GET /files/{id}/errors` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `GET /history` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `GET /shares/current` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |
| `GET /shares/versions` | UPLOAD_STATISTICS | 403 | 403 | 403 | 403 | 403 | 2xx |

## inventory

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `PUT /clinics/{id}/annual-consumption` | ENTER_ANNUAL_CONSUMPTION | 2xx | 403 | 403 | 403 | 403 | 403 |
| `GET /clinics/{id}/norms` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /clinics/{id}/stock` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `POST /clinics/{id}/sync` | SYNC_ON_DEMAND | 2xx | 403 | 403 | 403 | 403 | 403 |
| `GET /clinics/{id}/units` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `POST /dev/protected-volume` | DEV_PANEL | 2xx | 2xx | 2xx | 2xx | 403 | 2xx |
| `GET /internal/units/status` | INTERNAL_UNIT_STATUS | 403 | 403 | 403 | 403 | 2xx | 403 |
| `GET /sync/last` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /sync/runs` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /units/{id}` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /units/{id}/history` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |

## notification

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /notifications` | READ_AUDIT | 403 | 2xx | 2xx | 403 | 403 | 403 |

## transfer

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /obligations` | VIEW_OBLIGATION | 2xx | 2xx | 2xx | 2xx | 2xx | 403 |
| `POST /obligations` | OPEN_OBLIGATION | 403 | 403 | 403 | 403 | 2xx | 403 |
| `GET /obligations/{id}` | VIEW_OBLIGATION | 2xx | 2xx | 2xx | 2xx | 2xx | 403 |
| `POST /obligations/{id}/cancel` | CANCEL_OBLIGATION | 403 | 2xx | 403 | 403 | 2xx | 403 |
| `POST /obligations/{id}/confirm` | CONFIRM_TRANSFER | 2xx | 2xx | 403 | 403 | 403 | 403 |
| `GET /obligations/{id}/history` | VIEW_OBLIGATION | 2xx | 2xx | 2xx | 2xx | 2xx | 403 |
| `POST /obligations/{id}/reject` | REJECT_TRANSFER | 2xx | 2xx | 403 | 403 | 403 | 403 |
| `POST /obligations/{id}/replace` | REPLACE_UNIT | 2xx | 2xx | 403 | 403 | 403 | 403 |

## web

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /events/obligations` | VIEW_OBLIGATION | 2xx | 2xx | 2xx | 2xx | 2xx | 403 |
| `GET /tasks` | VIEW_TASKS | 2xx | 2xx | 403 | 403 | 403 | 403 |
| `GET /tasks/{id}` | VIEW_OBLIGATION | 2xx | 2xx | 2xx | 2xx | 2xx | 403 |
| `POST /tasks/{id}/confirm` | CONFIRM_TRANSFER | 2xx | 2xx | 403 | 403 | 403 | 403 |
| `POST /tasks/{id}/reject` | REJECT_TRANSFER | 2xx | 2xx | 403 | 403 | 403 | 403 |
| `POST /tasks/{id}/replace` | REPLACE_UNIT | 2xx | 2xx | 403 | 403 | 403 | 403 |

Публичные, без проверки прав: `GET /`, `GET /auth/callback`, `GET /auth/login`, `GET /login`, `POST /logout`.
