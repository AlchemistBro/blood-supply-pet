-- +goose Up
-- Неизменяемый журнал аудита (NFR008, NFR016): записи только добавляются, каждая связана с предыдущей хешем.

CREATE TABLE audit_record (
    audit_record_id   uuid        PRIMARY KEY,
    seq               bigint      NOT NULL UNIQUE,
    occurred_at       timestamptz NOT NULL,
    received_at       timestamptz NOT NULL,
    user_id           text,
    role              text,
    clinic_id         bigint,
    module            text        NOT NULL,
    action            text        NOT NULL,
    object_type       text        NOT NULL,
    object_id         text        NOT NULL,
    result            text        NOT NULL CHECK (result IN ('SUCCESS', 'DENIED', 'ERROR')),
    details           jsonb,
    correlation_id    text,
    producer          text        NOT NULL,
    source_message_id uuid        NOT NULL UNIQUE,
    prev_hash         bytea       NOT NULL,
    hash              bytea       NOT NULL
);
CREATE INDEX audit_record_object_idx ON audit_record (object_type, object_id, seq);
CREATE INDEX audit_record_occurred_idx ON audit_record (occurred_at);
CREATE INDEX audit_record_module_action_idx ON audit_record (module, action, seq);
CREATE INDEX audit_record_user_idx ON audit_record (user_id, seq) WHERE user_id IS NOT NULL;

-- Голова цепочки: одна строка, блокируется при добавлении, чтобы seq и hash шли строго по порядку.
CREATE TABLE audit_chain_head (
    id   integer PRIMARY KEY CHECK (id = 1),
    seq  bigint  NOT NULL,
    hash bytea   NOT NULL
);
INSERT INTO audit_chain_head (id, seq, hash) VALUES (1, 0, decode('0000000000000000000000000000000000000000000000000000000000000000', 'hex'));

-- Запрет изменений и удалений на уровне СУБД: журнал только добавляется.
-- +goose StatementBegin
CREATE FUNCTION audit_record_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'журнал аудита неизменяем: % запрещено', TG_OP;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_record_no_update BEFORE UPDATE OR DELETE ON audit_record
    FOR EACH ROW EXECUTE FUNCTION audit_record_immutable();

-- +goose Down
DROP TRIGGER audit_record_no_update ON audit_record;
DROP FUNCTION audit_record_immutable();
DROP TABLE audit_chain_head;
DROP TABLE audit_record;
