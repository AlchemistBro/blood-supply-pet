-- +goose Up
-- Схема сервиса notification (база notification). Платформенные таблицы (outbox, inbox, audit_local, ref_*)
-- создаются миграциями 00001-00099.

-- Журнал писем (ИНТ-04, C04). Адресат — служебный ящик подразделения из статического справочника, не личный адрес
-- (ADR-0021). Текст письма не хранится: письмо собирается по шаблону из типизированных полей ниже, поэтому поля
-- свободного текста, куда мог бы попасть номер случая пациента или фенотип, нет (FR044, NFR016, ADR-0011).
CREATE TABLE notification (
    notification_id     uuid        PRIMARY KEY,
    kind                text        NOT NULL CHECK (kind IN ('URGENT_TASK', 'ESCALATION', 'TRANSFER_BROKEN')),
    obligation_id       uuid        NOT NULL,
    request_id          uuid,
    -- Сообщение transfer.events, из которого родилось письмо: одно событие — не больше одного письма адресату.
    source_message_id   uuid        NOT NULL,
    recipient_role      text        NOT NULL CHECK (recipient_role IN ('CLINIC_OPERATOR', 'DUTY_OFFICER')),
    recipient_clinic_id bigint,
    -- NULL — адресат для роли и клиники не настроен; такая запись сразу FAILED.
    recipient_email     text,
    subject             text        NOT NULL,
    source_clinic_id    bigint,
    target_clinic_id    bigint,
    component_kind_code text,
    quantity            integer     CHECK (quantity IS NULL OR quantity > 0),
    urgency_code        text,
    deadline            timestamptz,
    reason              text,
    status              text        NOT NULL CHECK (status IN ('PENDING', 'SENT', 'FAILED')),
    attempts            integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at     timestamptz NOT NULL,
    -- Только ответ SMTP-сервера или описание сетевой ошибки, без текста письма.
    last_error          text,
    created_at          timestamptz NOT NULL,
    sent_at             timestamptz,
    CONSTRAINT notification_once_per_recipient UNIQUE (source_message_id, kind, recipient_role),
    CONSTRAINT notification_email_when_sent CHECK (status <> 'SENT' OR (recipient_email IS NOT NULL AND sent_at IS NOT NULL))
);

-- Отправщик выбирает созревшие записи: только PENDING, по времени следующей попытки.
CREATE INDEX notification_due_idx ON notification (next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX notification_obligation_idx ON notification (obligation_id);
CREATE INDEX notification_status_created_idx ON notification (status, created_at DESC);

-- Проекция заданий из ObligationOpened: сведения для писем об эскалации и нарушении, в событиях которых их нет.
-- Событие об открытии приходит один раз на обязательство (ключ Kafka — obligation_id, порядок в партиции сохраняется),
-- поэтому версия не нужна: повтор не перезаписывает строку.
CREATE TABLE obligation_projection (
    obligation_id       uuid        PRIMARY KEY,
    request_id          uuid        NOT NULL,
    origin              text        NOT NULL,
    source_clinic_id    bigint      NOT NULL,
    target_clinic_id    bigint      NOT NULL,
    component_kind_code text        NOT NULL,
    requested_qty       integer     NOT NULL,
    -- В ObligationOpened v1 срочности нет; NULL — неизвестна.
    urgency_code        text,
    deadline            timestamptz NOT NULL,
    opened_at           timestamptz NOT NULL
);

-- +goose Down
DROP TABLE obligation_projection;
DROP TABLE notification;
