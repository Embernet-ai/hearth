-- Time off: somebody asks for days away, their approver says yes or no.
--
-- It came over from ClickUp as plain HR documents filled in from a form, which
-- meant nothing routed a request to anybody and nothing knew when somebody was
-- out. This is that form, with the routing: the same people who approve a
-- person's hours decide their time off (timelog's approver rule), plus anybody
-- holding the People capability, which is HR.
--
-- Private by default, because a leave reason is often medical: a request is
-- visible to the person who made it, whoever may decide it, and People. What
-- the team sees is an all-day "Out" on the calendar once it is approved -- the
-- dates, never the reason (calendar_event_id points at it, so a later cancel
-- takes it off again).
--
-- days is working days, Monday to Friday inclusive, worked out by Hearth so
-- nobody has to count; it is stored so a request reads the same after the
-- rules for counting change.

-- +goose Up
CREATE TABLE time_off_requests (
    id                uuid        PRIMARY KEY,
    org_id            uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    requester_id      uuid        NOT NULL REFERENCES principals (id),
    starts_on         date        NOT NULL,
    ends_on           date        NOT NULL,
    days              integer     NOT NULL,
    kind              text        NOT NULL,
    reason            text        NOT NULL DEFAULT '',
    comments          text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'pending',
    decided_by        uuid        REFERENCES principals (id),
    decided_at        timestamptz,
    decision_note     text        NOT NULL DEFAULT '',
    calendar_event_id uuid,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT time_off_dates_check CHECK (ends_on >= starts_on),
    CONSTRAINT time_off_kind_check CHECK (kind IN ('vacation', 'sick', 'personal', 'medical',
        'family', 'bereavement', 'jury_duty', 'other')),
    CONSTRAINT time_off_status_check CHECK (status IN ('pending', 'approved', 'denied', 'cancelled'))
);

CREATE INDEX time_off_requester_idx ON time_off_requests (org_id, requester_id, starts_on DESC);
CREATE INDEX time_off_pending_idx   ON time_off_requests (org_id, status) WHERE status = 'pending';

-- +goose Down
DROP TABLE IF EXISTS time_off_requests;
