# Безопасность и доступ

## Идентификация (Keycloak, OIDC)

- Realm `blood-supply`, клиент `web` (confidential, Authorization Code + PKCE), клиент `services` (bearer-only) для проверки.
- Роли realm: `PHYSICIAN`, `CLINIC_OPERATOR`, `TERRITORIAL_ANALYST`, `DUTY_OFFICER`, `IT_SERVICE`.
- Атрибут пользователя `clinic_id` → claim `clinic_id`; роли → claim `realm_access.roles`; `preferred_username` используется как `user_id` в аудите.
- 2FA (OTP) для роли `PHYSICIAN` включается флагом realm `physician_mfa_required` (по умолчанию выключен в демо; ADR-0006).
- `web` хранит сессию в зашифрованной cookie (`SameSite=Lax`, `Secure`, `HttpOnly`), обновляет access token по refresh token, при выходе вызывает end-session Keycloak.
- Сервисы проверяют JWT (подпись по JWKS с кэшем, `iss`, `aud`, `exp`) в middleware `libs/httpx/auth`; внутренние вызовы (`transfer → inventory`) идут с сервисным токеном (client credentials) и помечены `X-Internal-Call`.

## Авторизация

Матрица «роль → операция» в `libs/httpx/auth/policy.go`, одна на всю систему, проверяется в каждом сервисе:

| Операция | PHYSICIAN | CLINIC_OPERATOR | TERRITORIAL_ANALYST | DUTY_OFFICER | IT_SERVICE |
| --- | --- | --- | --- | --- | --- |
| Просмотр запаса своей клиники | ✓ | ✓ | — | ✓ (все) | ✓ |
| Ввод годового расхода, FR050 | — | ✓ (своя клиника) | — | — | — |
| Загрузка CSV, публикация оценки | — | — | ✓ | — | — |
| Просмотр плана, коррекция прогноза (FR024) | — | ✓ | ✓ (просмотр) | ✓ | — |
| Подтверждение / замена / отказ по заданию | — | ✓ (источник = своя клиника) | — | ✓ | — |
| Создание заявки, выбор варианта, отмена | ✓ (своя клиника) | — | — | — | — |
| Чтение `patient_case_no` | ✓ (своя заявка) | ✓ (задание по заявке, только при подтверждении) | — | — | — |
| Эскалации, ручная перевозка | — | — | — | ✓ | — |
| Правила подбора O15 (версии) | просмотр | — | — | — | ✓ |
| Журнал аудита | — | — | — | ✓ | ✓ |
| Dev-панель имитаторов | только профиль `dev` | | | | |

Проверка клиники: `clinic_id` из токена сравнивается с клиникой объекта; несовпадение → 403 и запись `DENIED` в аудит.

## Данные класса K1

- `patient_case_no` шифрует приложение (`libs/k1seal`, ADR-0020). Конверт хранится в `matching` (`clinical_request_patient`)
  и в `aist_outbox_message.payload` (`inventory`, до квитанции АИСТ-М, затем затирается). Ключ `BS_K1_KEYS` есть только
  у `matching` и `inventory`; `transfer`, Kafka, outbox, аудит и журналы видят только конверт.
- Показ номера — только `web` → `matching` `GET /requests/{id}/patient-case`: `matching` проверяет роль и клинику
  (оператору — если его клиника источник активного задания по заявке), в одной транзакции пишет `READ_PATIENT_CASE`
  и только затем отдаёт номер. `web` получает его по HTTP (TLS в k8s) лишь для экрана: не хранит, не логирует, ключа не имеет.
- Открытым текстом номер не попадает никуда: ни в события и команды, ни в логи, трассы, письма, объяснения вариантов.
  Конверт (`patient_case_sealed`, рядом `request_id`) есть только в командах `OpenObligation` и `AllocateUnitsToPatient`.
- Каждое чтение — `READ_PATIENT_CASE` в аудит (NFR016); каждое изменение заявки — аудит с участником (NFR008).
- Спецификация замены (фенотип) — квазиидентификатор: не логируется, не уходит в письма.

## NFR010: белый список полей прогноза ЧС

Входной адаптер `planning/internal/adapters/forecastin` декодирует в строгую структуру (`DisallowUnknownFields`), затем
дополнительно отбрасывает всё, кроме: `scenario_version, territory, period_start, period_end, lines[].clinic_code?, lines[].component_kind,
lines[].blood_group?, lines[].volume_ml, lines[].share?`. Тест: запрос с полями `snils`, `full_name`, `patients[]` принимается
без них, в БД и логах их нет.

## Платформа

- TLS на входе (nginx / ingress), внутри кластера plain HTTP с NetworkPolicy; mTLS для ИНТ-05 в проде (в демо отключено, флаг).
- Kafka: в compose без аутентификации; в `prod-example` SASL/SCRAM и ACL на топики по владельцу.
- PostgreSQL: роль на сервис, `audit_writer` только INSERT, `REVOKE CONNECT FROM PUBLIC`.
- Образы distroless без shell, non-root, read-only FS; `govulncheck`, `trivy`, `gosec` в CI; dependabot.
- Секреты: `.env.example` и kustomize `secretGenerator` с демо-значениями; настоящие секреты никогда в git (gitleaks в CI).
- Лимиты частоты на ingress для `/auth` и `/api/int/forecast`.
