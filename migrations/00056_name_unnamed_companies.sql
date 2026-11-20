-- Companies that came in with no name, named by their web address.
--
-- The HubSpot import that fed Twenty (Fireball-Red-Team/iron tools/hsimport, importCompanies) wrote "(unnamed <HubSpot id>)" for a
-- company whose HubSpot record had no name, and Hearth's Twenty import carried that string across as it was. 232 companies are
-- named that way, every one with a web address. The real name was never in HubSpot, so none was lost on the way here; what is
-- left to show is the domain. The old text is kept in the company's notes, with the HubSpot id, so it can be found and undone.
-- The EmberOps sync still treats a company named only by its web address as having no real name (crmsync unnamed), so this does
-- not send 232 firms to EmberOps as customers.

-- +goose Up
UPDATE companies
   SET notes = CASE WHEN notes = '' THEN '' ELSE notes || E'\n' END ||
               'Named from its web address: it came from HubSpot with no name, as "' || name || '".',
       name = domain,
       updated_at = now()
 WHERE deleted_at IS NULL
   AND name ~ '^\(unnamed [0-9]+\)$'
   AND domain <> '';

-- +goose Down
UPDATE companies
   SET name = substring(notes FROM 'came from HubSpot with no name, as "(\(unnamed [0-9]+\))"\.'),
       notes = btrim(regexp_replace(notes, E'\\n?Named from its web address: it came from HubSpot with no name, as "\\(unnamed [0-9]+\\)"\\.', ''), E'\n'),
       updated_at = now()
 WHERE notes ~ 'Named from its web address: it came from HubSpot with no name, as "\(unnamed [0-9]+\)"\.';
