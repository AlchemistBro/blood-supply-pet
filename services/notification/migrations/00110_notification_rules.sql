-- +goose Up
ALTER TABLE notification DROP CONSTRAINT notification_kind_check;
ALTER TABLE notification ADD CONSTRAINT notification_kind_check CHECK(kind IN ('URGENT_TASK','ESCALATION','TRANSFER_BROKEN','TRANSFER_TERMINAL','STOCK_BELOW_NORM','SECURITY_INCIDENT'));
ALTER TABLE notification DROP CONSTRAINT notification_recipient_role_check;
ALTER TABLE notification ADD CONSTRAINT notification_recipient_role_check CHECK(recipient_role IN ('CLINIC_OPERATOR','DUTY_OFFICER','PHYSICIAN','IT_SERVICE'));
-- Для уведомлений вне O07 obligation_id = нулевой UUID; source_message_id сохраняет связь с событием.
-- +goose Down
DELETE FROM notification WHERE kind IN ('TRANSFER_TERMINAL','STOCK_BELOW_NORM','SECURITY_INCIDENT');
ALTER TABLE notification DROP CONSTRAINT notification_kind_check;
ALTER TABLE notification ADD CONSTRAINT notification_kind_check CHECK(kind IN ('URGENT_TASK','ESCALATION','TRANSFER_BROKEN'));
ALTER TABLE notification DROP CONSTRAINT notification_recipient_role_check;
ALTER TABLE notification ADD CONSTRAINT notification_recipient_role_check CHECK(recipient_role IN ('CLINIC_OPERATOR','DUTY_OFFICER'));
