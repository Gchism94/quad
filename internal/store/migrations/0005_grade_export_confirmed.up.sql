-- CC-CA15: purge-after-confirmed-export. export_confirmed_at is set only by the
-- explicit POST .../grades/confirm-export action, never by the CSV download
-- itself, so it is safe to use as the start of the retention countdown.
ALTER TABLE grades ADD COLUMN IF NOT EXISTS export_confirmed_at TIMESTAMPTZ;
