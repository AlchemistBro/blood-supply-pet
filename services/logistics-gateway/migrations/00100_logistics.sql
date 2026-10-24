-- +goose Up
-- Шлюз перевозчика C03 (ИНТ-03), база logistics (ADR-0005). transfer сюда не ходит: команды и ответы только через Kafka.
-- Имена полей по data-model.md, раздел logistics. Платформенные таблицы (outbox, inbox, audit_local, ref_*)
-- создаются миграциями 00001-00099.

-- Заказ перевозки по обязательству O07. Одна строка на обязательство: повтор команды упирается в первичный ключ
-- и второго заказа не создаёт. Маршрут и груз допускают NULL: «надгробие» (status = CANCELLED без маршрута)
-- вставляется, если CancelTransport обогнал RequestTransport, и поздняя команда в него упирается.
-- Внешних ключей на ref_* нет: маршрут — копия команды transfer, проверять его — дело отправителя.
CREATE TABLE transport_request (
    obligation_id       uuid        PRIMARY KEY,
    request_id          uuid,
    source_clinic_id    bigint,
    target_clinic_id    bigint,
    component_kind_code text,
    -- Число контейнеров без номеров единиц: замена единицы до отправки не трогает заказ (ADR-0011).
    quantity            integer     CHECK (quantity > 0),
    -- Режим транспортировки (ПП РФ № 641): перевозчик обязан знать условия перевозки; значения демо (ADR-0010).
    temperature_regime  text        CHECK (temperature_regime IN ('COLD_2_6', 'FROZEN_MINUS_25', 'ROOM_20_24')),
    status              text        NOT NULL
        CHECK (status IN ('REQUESTING', 'ACCEPTED', 'REJECTED', 'UNKNOWN', 'FAILED', 'CANCELLED')),
    shipment_ref        text,
    cancel_requested    boolean     NOT NULL DEFAULT false,
    attempts            integer     NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz NOT NULL,
    -- Аренда строки: с C03 по этой строке говорит только её владелец; истекает сама, если экземпляр упал.
    lease_owner         uuid,
    lease_until         timestamptz,
    last_error          text,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    -- Без маршрута бывает только надгробие отмены.
    CONSTRAINT transport_request_route_chk CHECK (
        status = 'CANCELLED'
        OR (request_id IS NOT NULL AND source_clinic_id IS NOT NULL AND target_clinic_id IS NOT NULL
            AND component_kind_code IS NOT NULL AND quantity IS NOT NULL AND temperature_regime IS NOT NULL)
    )
);

-- Очередь сверщика: строки, которым есть что делать с C03.
CREATE INDEX transport_request_due_idx ON transport_request (next_attempt_at)
    WHERE status IN ('REQUESTING', 'UNKNOWN') OR (status = 'ACCEPTED' AND cancel_requested);

-- Оценки времени доставки от C03 (ADR-0014). Заполняется на следующих этапах; здесь только таблица.
CREATE TABLE delivery_estimate (
    source_clinic_id bigint      NOT NULL,
    target_clinic_id bigint      NOT NULL,
    minutes          integer     NOT NULL CHECK (minutes >= 0),
    estimated_at     timestamptz NOT NULL,
    PRIMARY KEY (source_clinic_id, target_clinic_id)
);

-- +goose Down
DROP TABLE delivery_estimate;
DROP TABLE transport_request;
