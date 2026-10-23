-- +goose Up
-- Схема сервиса transfer (база transfer). Имена сущностей и полей - из словаря данных ПР6 (docs/architecture/data-model.md).
-- Платформенные таблицы (outbox, inbox, audit_local, ref_*) создаются миграциями 00001-00002.

-- +goose Down
