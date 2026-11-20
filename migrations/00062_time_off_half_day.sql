-- Time off: half a day.
--
-- Part of the request is a half day: the first day counts as half, the way an
-- HRMS Leave Application does with half_day, so a request in Hearth and its
-- Leave Application in EmberOps read the same. days stays the whole working
-- days the dates span (a half day on a one-day request is 0.5 of that one day);
-- half_day says whether the first one counts as a half.
--
-- This came with approvers and admins being able to change or cancel a request:
-- half a day is one of the things they change.

-- +goose Up
ALTER TABLE time_off_requests ADD COLUMN half_day boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE time_off_requests DROP COLUMN IF EXISTS half_day;
