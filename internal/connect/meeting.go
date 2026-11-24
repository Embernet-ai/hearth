package connect

import "context"

// CreateEvent puts an event on the person's own calendar at a provider and returns it, with a Meet or Teams link when
// meeting is set. It is how the CRM books a meeting from a record: the event is the person's, on their calendar, and
// the link comes back to be sent to the other side.
func (s *Service) CreateEvent(ctx context.Context, orgID, principalID string, p Provider, e Event, meeting bool) (Event, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Calendar)
	if err != nil {
		return Event{}, err
	}
	return sess.Calendar().Create(ctx, "primary", e, meeting)
}
