-- +goose Up
-- Режимы обмена с АИСТ-М (ИНТ-01, FR001, FR050): сеанс по клинике (ON_DEMAND), число расхождений со снимком
-- и порядковый номер сеанса — водяной знак в StockSnapshotPublished (номер последнего успешного сеанса).

ALTER TABLE sync_run
    ADD COLUMN clinic_id           bigint  REFERENCES ref_clinic (clinic_id),
    ADD COLUMN discrepancies_count integer NOT NULL DEFAULT 0 CHECK (discrepancies_count >= 0),
    ADD COLUMN sync_seq            bigint  GENERATED ALWAYS AS IDENTITY;
COMMENT ON COLUMN sync_run.clinic_id IS 'Клиника оператора для ON_DEMAND (FR050); NULL — сеанс по расписанию';
COMMENT ON COLUMN sync_run.discrepancies_count IS 'Расхождения копии со снимком и переходы вне схемы состояний (FR001, FR008)';
COMMENT ON COLUMN sync_run.sync_seq IS 'Номер сеанса; номер последнего успешного — водяной знак снимков запаса';
CREATE UNIQUE INDEX sync_run_seq_uq ON sync_run (sync_seq);

-- Водяной знак копии: последний успешный сеанс, получивший данные из АИСТ-М (изменения или полный снимок).
-- При недоступном API изменений актуальность держится на суточном снимке (FR001).
CREATE INDEX sync_run_data_watermark_idx ON sync_run (finished_at DESC)
    WHERE sync_result = 'SUCCESS' AND sync_kind IN ('CHANGES', 'ON_DEMAND', 'DAILY_SNAPSHOT');

-- +goose Down
DROP INDEX sync_run_data_watermark_idx;
DROP INDEX sync_run_seq_uq;
ALTER TABLE sync_run DROP COLUMN sync_seq, DROP COLUMN discrepancies_count, DROP COLUMN clinic_id;
