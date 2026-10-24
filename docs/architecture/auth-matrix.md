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

## inventory

| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |
| --- | --- | :---: | :---: | :---: | :---: | :---: | :---: |
| `GET /clinics/{id}/stock` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /clinics/{id}/units` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `POST /dev/protected-volume` | DEV_PANEL | 2xx | 2xx | 2xx | 2xx | 403 | 2xx |
| `GET /internal/units/status` | INTERNAL_UNIT_STATUS | 403 | 403 | 403 | 403 | 2xx | 403 |
| `GET /sync/last` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /sync/runs` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /units/{id}` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
| `GET /units/{id}/history` | VIEW_STOCK | 2xx | 2xx | 2xx | 2xx | 403 | 403 |
