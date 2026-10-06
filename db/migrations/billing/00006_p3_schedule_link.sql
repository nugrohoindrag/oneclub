-- PRD P3 FR-APP-P3-07 / FR-WEB-P3-05 / FR-INT-P3-04: a secure payment link
-- per payment schedule, so a DP or termin is paid online (website, reminder
-- e-mail) without staff issuing an invoice first. Existing schedules get a
-- token too (gen_random_uuid is a CSPRNG; 32 random hex characters each).

-- +goose Up
ALTER TABLE billing.payment_schedules
  ADD COLUMN public_token text NOT NULL DEFAULT replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '');
ALTER TABLE billing.payment_schedules ADD CONSTRAINT payment_schedules_public_token UNIQUE (public_token);

-- +goose Down
ALTER TABLE billing.payment_schedules DROP COLUMN public_token;
