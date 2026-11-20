-- Department requests: somebody says which departments they belong in, and a
-- manager of those departments, or HR, says yes, no, or "yes, but these".
--
-- Until now only an admin could put somebody in a department, and nobody was
-- asked, so a new person sat in none: their hours went to nobody to approve and
-- they could not see the department they were hired into. This is the question,
-- routed to the people who can answer it.
--
-- What a yes gives is membership (principal_departments), and membership is a
-- view of the department -- open it, file work into it -- never a grant: a
-- space's grants are copied onto every document filed in it, and a new member
-- should see their own work there, not everybody's. Full access stays a
-- separate, deliberate grant on the People page.
--
-- The departments asked for and the departments granted are both kept, as
-- rows of department_request_spaces with kind 'asked' or 'granted', so the
-- record says what the person wanted and what the decider changed.

-- +goose Up
CREATE TABLE department_requests (
    id            uuid        PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    requester_id  uuid        NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    note          text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'pending',
    decided_by    uuid        REFERENCES principals (id) ON DELETE SET NULL,
    decided_at    timestamptz,
    decision_note text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT department_requests_status_check
        CHECK (status IN ('pending', 'approved', 'denied', 'cancelled'))
);

-- One open question per person. A second request replaces the first rather
-- than queueing behind it.
CREATE UNIQUE INDEX department_requests_one_pending
    ON department_requests (requester_id) WHERE status = 'pending';
CREATE INDEX department_requests_pending_idx
    ON department_requests (org_id) WHERE status = 'pending';

CREATE TABLE department_request_spaces (
    request_id uuid NOT NULL REFERENCES department_requests (id) ON DELETE CASCADE,
    space_id   uuid NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    kind       text NOT NULL,

    PRIMARY KEY (request_id, kind, space_id),
    CONSTRAINT department_request_spaces_kind_check CHECK (kind IN ('asked', 'granted'))
);
CREATE INDEX department_request_spaces_space_idx ON department_request_spaces (space_id);

-- +goose Down
DROP TABLE IF EXISTS department_request_spaces;
DROP TABLE IF EXISTS department_requests;
