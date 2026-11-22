package connect

import (
	"context"
	"errors"
	"time"
)

// SyncPerson runs every pull connector that is on for one person at one provider, and records what each did. A
// failure in one does not stop the others: a mailbox the person did not grant mail access to is not a reason to
// leave their calendar stale.
func (s *Service) SyncPerson(ctx context.Context, orgID, principalID string, p Provider) map[Kind]error {
	res := map[Kind]error{}
	for _, k := range Kinds {
		if !k.Synced() || s.UnavailableReason(k) != "" {
			continue
		}
		on, err := s.Enabled(ctx, orgID, p, k)
		if err != nil || !on {
			continue
		}
		res[k] = s.SyncOne(ctx, orgID, principalID, p, k)
	}
	return res
}

// SyncOne runs one connector for one person now.
func (s *Service) SyncOne(ctx context.Context, orgID, principalID string, p Provider, k Kind) error {
	var n int
	var err error
	switch k {
	case Calendar:
		n, err = s.SyncCalendar(ctx, orgID, principalID, p)
	case Contacts:
		n, err = s.SyncContacts(ctx, orgID, principalID, p)
	case Mail:
		n, err = s.SyncMail(ctx, orgID, principalID, p)
	default:
		return errors.New("connect: that connector is not synced")
	}
	if errors.Is(err, ErrDisabled) {
		return err
	}
	s.record(ctx, orgID, principalID, p, k, n, humanize(err))
	return err
}

// humanize turns an error into what an administrator reads in the status column.
func humanize(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrScope):
		return errors.New("the person has not granted this permission; they connect again and tick it")
	case errors.Is(err, ErrReconnect):
		return errors.New("the connection was revoked; the person connects again")
	case errors.Is(err, ErrNotConnected):
		return errors.New("not connected")
	}
	return err
}

// advisoryKey is the lock that keeps two replicas from running the same pass.
const advisoryKey = 0x68656172_74685f63 // "hearth_c"

// RunAll is one scheduled pass over everybody who is connected. A second replica that finds the lock taken does
// nothing, so running more than one Hearth never doubles the calls to a provider.
func (s *Service) RunAll(ctx context.Context) {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(advisoryKey)).Scan(&got); err != nil || !got {
		return
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(advisoryKey))
	}()

	for _, p := range Providers {
		cs, err := s.connected(ctx, p)
		if err != nil {
			s.log.Warn("connect: list connections", "provider", p, "error", err)
			continue
		}
		for _, c := range cs {
			if ctx.Err() != nil {
				return
			}
			if c.Revoked {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			for k, err := range s.SyncPerson(pctx, c.OrgID, c.PrincipalID, p) {
				if err != nil {
					s.log.Warn("connect: sync", "provider", p, "connector", k, "person", c.PrincipalID, "error", err)
				}
			}
			cancel()
		}
	}
}

// Run calls RunAll every interval until the context ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunAll(ctx)
		}
	}
}
