-- +goose Up
-- Платформа обмена (ADR-0003). Таблицы одинаковы в базе каждого сервиса.

-- Outbox: сообщение пишется в транзакции бизнес-операции, ретранслятор отправляет по порядку id.
CREATE TABLE outbox (
    id             bigserial   PRIMARY KEY,
    message_id     uuid        NOT NULL UNIQUE,
    topic          text        NOT NULL,
    key            text        NOT NULL,
    message_type   text        NOT NULL,
    schema_version integer     NOT NULL DEFAULT 1,
    payload        jsonb       NOT NULL,
    correlation_id text,
    causation_id   text,
    producer       text        NOT NULL,
    occurred_at    timestamptz NOT NULL,
    published_at   timestamptz
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

-- Inbox: принятые сообщения; повтор доставки упирается в первичный ключ.
CREATE TABLE inbox (
    message_id   uuid        PRIMARY KEY,
    message_type text        NOT NULL,
    received_at  timestamptz NOT NULL
);

-- Локальный аудит в транзакции (NFR008); в неизменяемый журнал уходит через outbox.
CREATE TABLE audit_local (
    id             bigserial   PRIMARY KEY,
    occurred_at    timestamptz NOT NULL,
    user_id        text,
    role           text,
    clinic_id      bigint,
    action         text        NOT NULL,
    object_type    text        NOT NULL,
    object_id      text        NOT NULL,
    result         text        NOT NULL CHECK (result IN ('SUCCESS', 'DENIED', 'ERROR')),
    details        jsonb,
    correlation_id text
);
CREATE INDEX audit_local_object_idx ON audit_local (object_type, object_id);

-- +goose Down
DROP TABLE audit_local;
DROP TABLE inbox;
DROP TABLE outbox;
