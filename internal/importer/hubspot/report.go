package hubspot

import (
	"fmt"
	"io"
	"sort"
	"time"
)

// Print writes the report a person reads before saying go.
func (r *Report) Print(w io.Writer) {
	mode := "DRY RUN: nothing was written"
	if r.Apply {
		mode = "APPLIED"
	}
	fmt.Fprintf(w, "HubSpot import, %s\n\n", mode)
	fmt.Fprintf(w, "%-10s %-20s %6s %7s %5s %7s %6s %5s %9s %6s\n", "object", "read since", "read", "create", "link", "update", "same", "skip", "conflicts", "failed")
	for _, o := range Order {
		c, ok := r.Objects[o]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "%-10s %-20s %6d %7d %5d %7d %6d %5d %9d %6d\n", o, r.Since[o].Format(time.RFC3339), c.Read, c.Create, c.Link,
			c.Update, c.Same, c.Skip, c.Conflicts, c.Failed)
		if c.Pulled > 0 {
			fmt.Fprintf(w, "           %d more companies were read by id because a contact or deal points at them and Hearth lacked them\n", c.Pulled)
		}
		var whys []string
		for why := range c.SkipWhy {
			whys = append(whys, why)
		}
		sort.Strings(whys)
		for _, why := range whys {
			fmt.Fprintf(w, "           skipped %d: %s\n", c.SkipWhy[why], why)
		}
	}
	for _, o := range Order {
		m := r.Fields[o]
		if len(m) == 0 {
			continue
		}
		var names []string
		for f := range m {
			names = append(names, f)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "\n%s fields written (into an empty field / replacing a value):\n", o)
		for _, f := range names {
			fmt.Fprintf(w, "  %-12s %6d / %d\n", f, m[f].Fill, m[f].Replace)
		}
	}
	if len(r.Replaced) > 0 {
		fmt.Fprintf(w, "\nvalues that replaced another (first %d):\n", len(r.Replaced))
		for _, c := range r.Replaced {
			fmt.Fprintf(w, "  %s %s %s: %q -> %q\n", c.Object, c.HubSpotID, c.Field, shorten(c.From), shorten(c.To))
		}
	}
	fmt.Fprintf(w, "\nowners: %s; %d HubSpot owners mapped to a Hearth account\n", r.OwnerSource, r.OwnersMapped)
	if len(r.OwnersUnmapped) > 0 {
		var ids []string
		total := 0
		for id, n := range r.OwnersUnmapped {
			ids = append(ids, id)
			total += n
		}
		sort.Strings(ids)
		fmt.Fprintf(w, "  %d record owner references name %d HubSpot owners with no Hearth account; those records are left unowned\n", total, len(ids))
		for i, id := range ids {
			if i == 15 {
				fmt.Fprintf(w, "  ... and %d more\n", len(ids)-15)
				break
			}
			fmt.Fprintf(w, "  owner %s: %d\n", id, r.OwnersUnmapped[id])
		}
	}
	for label, n := range r.UnknownStages {
		fmt.Fprintf(w, "\ndeal stage %q is not in the stage map (%d deals): it is left as it is, or new for a new deal\n", label, n)
	}
	for o, why := range r.NotCarried {
		fmt.Fprintf(w, "\nnot carried, %s: %s\n", o, why)
	}
	if r.ConflictTotal > 0 {
		fmt.Fprintf(w, "\nconflicts: %d fields left as they are in Hearth (first %d shown)\n", r.ConflictTotal, len(r.Conflicts))
		for _, c := range r.Conflicts {
			fmt.Fprintf(w, "  %s %s %s: Hearth %q, HubSpot %q (%s)\n", c.Object, c.HubSpotID, c.Field, shorten(c.Hearth), shorten(c.HubSpot), c.Trust)
		}
	}
	if r.ProblemTotal > 0 {
		fmt.Fprintf(w, "\nproblems: %d (first %d shown)\n", r.ProblemTotal, len(r.Problems))
		for _, p := range r.Problems {
			fmt.Fprintln(w, "  "+p)
		}
	}
	if len(r.HighWater) > 0 {
		fmt.Fprintln(w)
		for _, o := range Order {
			if t, ok := r.HighWater[o]; ok {
				verb := "would remember"
				if r.Apply && r.Objects[o].Failed == 0 {
					verb = "remembered"
				} else if r.Apply {
					verb = "NOT moved (failures)"
				}
				fmt.Fprintf(w, "high-water mark, %s: %s %s\n", o, verb, t.Format(time.RFC3339))
			}
		}
	}
}

func shorten(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return s
}
