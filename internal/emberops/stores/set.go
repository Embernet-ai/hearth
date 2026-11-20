package stores

import (
	"context"
	"time"

	"github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/maint"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/rules"
	"github.com/Embernet-ai/hearth/internal/teams"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
	"github.com/Embernet-ai/hearth/internal/views"
)

// Set is every domain store Hearth's server holds. In the default back end it
// is the Postgres services; with EmberOPS it is the same fields with the
// domains EmberOPS owns swapped for stores in this package. The server wires
// from a Set and never knows which it has.
type Set struct {
	Docs     docs.Store
	Views    views.Store
	Rules    rules.Store
	Teams    teams.Store
	Time     timelog.Store
	TimeOff  timeoff.Store
	Maint    maint.Store
	Plant    plant.Store
	CRM      crm.Store
	DeptReqs deptreq.Store
	Calendar calendar.Store
	Canvas   canvas.Store
}

// PG is the Postgres services, which are both the default Set and the
// fallback for whatever an EmberOPS store does not cover.
type PG struct {
	Docs     *docs.Service
	Views    *views.Service
	Rules    *rules.Service
	Teams    *teams.Service
	Time     *timelog.Service
	TimeOff  *timeoff.Service
	Maint    *maint.Service
	Plant    *plant.Service
	CRM      *crm.Service
	DeptReqs *deptreq.Service
	Calendar *calendar.Service
	Canvas   *canvas.Service
}

// Postgres is the standalone back end.
func Postgres(p PG) Set {
	return Set{
		Docs: p.Docs, Views: p.Views, Rules: p.Rules, Teams: p.Teams, Time: p.Time, TimeOff: p.TimeOff,
		Maint: p.Maint, Plant: p.Plant, CRM: p.CRM, DeptReqs: p.DeptReqs, Calendar: p.Calendar, Canvas: p.Canvas,
	}
}

// Options are what the EmberOPS stores need beyond the client.
type Options struct {
	Loc           *time.Location
	TimeOffNotify timeoff.Notifier
	DeptReqNotify deptreq.Notifier
	// Authz is Hearth's own access control, which still decides who may open
	// what: *authz.Service.
	Authz Authorizer
	// CurrencyPerCent is the value of one cent in the site's currency (0.01 for dollars).
	CurrencyPerCent float64
	// Spaces copies the space structure into EmberOPS; nil leaves it alone.
	Spaces *SpaceSync
	// CRMPartiesInEmberOPS moves companies and contacts into EmberOPS, which turns
	// Hearth's sales side (deals, owners, types, tags, the timeline) off. The
	// default is false: they stay in Postgres, and crmsync projects them.
	CRMPartiesInEmberOPS bool
}

// Authorizer is every question the EmberOPS stores put to Hearth's access
// control.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
	HasCapability(ctx context.Context, orgID, principalID, capability string) (bool, error)
	Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error
}

// EmberOPS is the EmberOPS back end: the domains listed in
// docs/emberops-backend.md come from EmberOPS, the rest from Postgres.
func EmberOPS(c *emberops.Client, p PG, o Options) Set {
	set := Postgres(p)
	b := Base{C: c, Loc: o.Loc}
	dir := Directory{C: c}
	set.Docs = &Docs{Store: p.Docs, Base: b, Dir: dir}
	set.Views = &Views{Base: b, Dir: dir}
	set.Time = &TimeLog{Store: p.Time, Base: b, Dir: dir, Authz: o.Authz, Docs: p.Docs}
	set.Rules = &Rules{Base: b, Dir: dir, Events: p.Rules}
	set.Calendar = &Calendar{Store: p.Calendar, Base: b, Dir: dir, PG: p.Calendar}
	set.Teams = &TeamsMirror{Store: p.Teams, Base: b, Dir: dir}
	set.DeptReqs = &DeptReqMirror{Store: p.DeptReqs, Base: b, Dir: dir}
	set.Canvas = &CanvasMirror{Store: p.Canvas, Base: b}
	set.CRM = &CRM{Store: p.CRM, Base: b, Dir: dir, Authz: o.Authz, Docs: p.Docs, CurrencyPerCent: o.CurrencyPerCent}
	if !o.CRMPartiesInEmberOPS {
		set.CRM = &partiesInPostgres{CRM: set.CRM.(*CRM), pg: p.CRM}
	}
	set.Maint = &Maint{Base: b, Dir: dir, Authz: o.Authz}
	set.Plant = &Plant{Store: p.Plant, Base: b, Dir: dir, Authz: o.Authz, Spaces: o.Spaces, CurrencyPerCent: o.CurrencyPerCent}
	set.TimeOff = &TimeOff{Base: b, Dir: dir, PG: p.TimeOff, Notify: o.TimeOffNotify}
	return set
}
