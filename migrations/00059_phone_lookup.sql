-- +goose Up

-- Who is calling: a phone number maps to a contact.
--
-- Numbers are free text in Hearth, typed by people and imported from other tools, so "(216) 555-0142",
-- "216-555-0142" and "+1 216 555 0142" are three spellings of one number. phone_e164 turns any of them
-- into E.164 so a caller id can be looked up. It is IMMUTABLE so the lookup can be an index and every write
-- path (the pages, the API, the importers, the syncs) is covered without a trigger or a second column to
-- keep in step.
--
-- The rule, and its one assumption: a leading + or 00 means the number already carries its country code and
-- is kept as written; ten digits, or eleven starting with 1, are North American (+1). Anything else is
-- ambiguous and normalizes to '' (not indexed, never matched) rather than guessing a country.
-- +goose StatementBegin
CREATE FUNCTION phone_e164(p text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN d = '' THEN ''
        WHEN p ~ '^\s*\+' AND length(d) BETWEEN 8 AND 15 THEN '+' || d
        WHEN d ~ '^00' AND length(d) BETWEEN 10 AND 17 THEN '+' || substr(d, 3)
        WHEN length(d) = 10 THEN '+1' || d
        WHEN length(d) = 11 AND d ~ '^1' THEN '+' || d
        ELSE ''
    END
    FROM (SELECT regexp_replace(coalesce(p, ''), '[^0-9]', '', 'g') AS d) x
$$;
-- +goose StatementEnd

-- Every number a person holds, normalized: the first plus the extras.
-- +goose StatementBegin
-- SET search_path FROM CURRENT because an index builds with a locked down search path and this calls phone_e164.
CREATE FUNCTION phone_e164_all(p text, extra text[]) RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path FROM CURRENT AS $$
    SELECT coalesce(array_agg(DISTINCT n) FILTER (WHERE n <> ''), '{}')
    FROM (SELECT phone_e164(p) AS n
          UNION ALL
          SELECT phone_e164(e) FROM unnest(coalesce(extra, '{}')) AS e) q
$$;
-- +goose StatementEnd

CREATE INDEX contacts_phone_e164_idx ON contacts USING gin (phone_e164_all(phone, extra_phones))
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS contacts_phone_e164_idx;
DROP FUNCTION IF EXISTS phone_e164_all(text, text[]);
DROP FUNCTION IF EXISTS phone_e164(text);
