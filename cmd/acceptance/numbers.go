package main

// The dashboard's numbers are records, not decoration. This creates a task that is overdue and assigned to the runner, logs half
// an hour, and (where the deployment has the sales pages) opens a deal and wins another; then it reads the dashboard and checks
// that each number moved by exactly that, that each tile's link lands on a list that holds what the tile counted, and that
// somebody with no permission to see a colleague's numbers is refused. Everything it made is removed again.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type numbersTile struct {
	Label, Value, Href string
}

type numbersPage struct {
	Page struct {
		Identity struct{ PrincipalID string }
		Dash     struct {
			ReportID string
			Stats    struct {
				Self   bool
				People []struct{ ID, Name string }
				Groups []struct {
					Key   string
					Tiles []numbersTile
				}
			}
		}
	}
}

// tiles reads the dashboard (for the person asked about, or the runner) into label to tile.
func (r *runner) tiles(person string) (map[string]numbersTile, *numbersPage, int) {
	path := "/"
	if person != "" {
		path += "?person=" + url.QueryEscape(person)
	}
	resp, err := r.raw(path)
	if err != nil {
		return nil, nil, 0
	}
	defer resp.Body.Close()
	var p numbersPage
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&p) != nil {
		return nil, nil, resp.StatusCode
	}
	out := map[string]numbersTile{}
	for _, g := range p.Page.Dash.Stats.Groups {
		for _, t := range g.Tiles {
			out[g.Key+"/"+t.Label] = t
		}
	}
	return out, &p, 200
}

var hoursRe = regexp.MustCompile(`^(?:(\d+)h)?\s*(?:(\d+)m)?$`)

// minutesOf reads "1h 30m", "2h" and "45m" back into minutes.
func minutesOf(s string) int {
	m := hoursRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	return h*60 + mi
}

// moneyOf reads "$1,250" or "$1,250.50" into cents.
func moneyOf(s string) int {
	s = strings.NewReplacer("$", "", ",", "").Replace(strings.TrimSpace(s))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return int(f*100 + 0.5)
}

// raw reads a page's data as the server sent it, not drawn.
func (r *runner) raw(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, r.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Acceptance-Raw", "1")
	return r.client.Do(req)
}

func (r *runner) theDashboardNumbersAreRecords() {
	const name = "the dashboard's numbers are the records, and each links to them"
	before, p, code := r.tiles("")
	if before == nil {
		r.fail(name, fmt.Sprintf("could not read the dashboard (HTTP %d)", code))
		return
	}
	me := p.Page.Identity.PrincipalID
	stamp := nonce()
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	var cleanup []string
	defer func() {
		for _, path := range cleanup {
			if resp, err := r.client.PostForm(r.base+path, url.Values{}); err == nil {
				resp.Body.Close()
			}
		}
	}()

	// A task that is overdue and assigned to the runner.
	title := "Acceptance: numbers " + stamp
	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind": {"task"}, "title": {title}, "assignee": {me}, "due": {yesterday}, "status": {"todo"},
	})
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	resp.Body.Close()
	m := docIDRe.FindStringSubmatch(resp.Request.URL.Path)
	if m == nil {
		r.fail(name, "the task was not created: "+resp.Request.URL.Path)
		return
	}
	cleanup = append(cleanup, "/documents/"+m[1]+"/delete")

	// Half an hour, on today.
	note := "numbers " + stamp
	resp, err = r.client.PostForm(r.base+"/time", url.Values{
		"minutes": {"0:30"}, "note": {note}, "on": {time.Now().Format("2006-01-02")},
		"week": {time.Now().Format("2006-01-02")},
	})
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	resp.Body.Close()

	after, _, _ := r.tiles("")
	if after == nil {
		r.fail(name, "could not read the dashboard again")
		return
	}
	num := func(m map[string]numbersTile, key string) int {
		n, _ := strconv.Atoi(m[key].Value)
		return n
	}
	if got, want := num(after, "work/Open and assigned"), num(before, "work/Open and assigned")+1; got != want {
		r.fail(name, fmt.Sprintf("open and assigned = %d, want %d", got, want))
		return
	}
	if got, want := num(after, "work/Overdue"), num(before, "work/Overdue")+1; got != want {
		r.fail(name, fmt.Sprintf("overdue = %d, want %d", got, want))
		return
	}
	if got, want := minutesOf(after["hours/Logged this week"].Value), minutesOf(before["hours/Logged this week"].Value)+30; got != want {
		r.fail(name, fmt.Sprintf("logged this week = %d minutes, want %d", got, want))
		return
	}
	if got, want := minutesOf(after["hours/Logged this month"].Value), minutesOf(before["hours/Logged this month"].Value)+30; got != want {
		r.fail(name, fmt.Sprintf("logged this month = %d minutes, want %d", got, want))
		return
	}

	// A daily report, if the runner has not filed today's: one more day with a report.
	if p.Page.Dash.ReportID == "" {
		if why := r.dailyReportNumber(stamp, before, &cleanup); why != "" {
			r.fail(name, why)
			return
		}
	}

	// Each tile's link lands on a list that holds what it counted.
	for _, c := range []struct{ tile, holds string }{
		{"work/Overdue", title}, {"work/Open and assigned", title},
		{"hours/Logged this week", note}, {"hours/Logged this month", note},
	} {
		resp, err := r.client.Get(r.base + after[c.tile].Href)
		if err != nil {
			r.fail(name, err.Error())
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), c.holds) {
			r.fail(name, fmt.Sprintf("%q links to %s, which does not hold %q (HTTP %d)", c.tile, after[c.tile].Href, c.holds, resp.StatusCode))
			return
		}
	}
	// And the timing entry is found so it can be removed.
	if resp, err := r.raw(after["hours/Logged this week"].Href); err == nil {
		var tp struct {
			Page struct{ MyTime []struct{ ID, Note string } }
		}
		_ = json.NewDecoder(resp.Body).Decode(&tp)
		resp.Body.Close()
		for _, e := range tp.Page.MyTime {
			if e.Note == note {
				cleanup = append(cleanup, "/time/entries/"+e.ID+"/delete")
			}
		}
	}

	// Deals, where this deployment has the sales pages: one open worth $1,250, one won worth $400 today.
	if resp, err := r.client.Get(r.base + "/deals"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			if why := r.dealNumbers(me, stamp, before, &cleanup); why != "" {
				r.fail(name, why)
				return
			}
		}
	}

	// Somebody with no permission to see a colleague's numbers is refused, and sees only their own.
	r.ensureSecondAccount()
	other, err := r.signInAs(secondEmail, secondPassword)
	if err == nil {
		if _, _, status := (&runner{base: r.base, client: other.http}).tiles(me); status != 403 {
			r.fail(name, fmt.Sprintf("a colleague asking for somebody else's numbers got HTTP %d, want 403", status))
			return
		}
		if mine, _, _ := (&runner{base: r.base, client: other.http}).tiles(""); mine == nil || len(mine) == 0 {
			r.fail(name, "a colleague could not see their own numbers")
			return
		}
	}
	r.check(name, true, "tasks, hours and deals move by exactly what was recorded, and every tile's link holds it")
}

// dailyReportNumber files a daily report and checks the "Daily reports filed" tile moved by one and links to it.
func (r *runner) dailyReportNumber(stamp string, before map[string]numbersTile, cleanup *[]string) string {
	resp, err := r.raw("/reports/daily/new")
	if err != nil {
		return err.Error()
	}
	var form struct {
		Page struct{ RepTree []struct{ ID string } }
	}
	_ = json.NewDecoder(resp.Body).Decode(&form)
	resp.Body.Close()
	if len(form.Page.RepTree) == 0 {
		return "no department to file a daily report in"
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("space", form.Page.RepTree[0].ID)
	_ = mw.WriteField("body", "Acceptance numbers report "+stamp)
	_ = mw.Close()
	resp, err = r.client.Post(r.base+"/reports/daily", mw.FormDataContentType(), &buf)
	if err != nil {
		return err.Error()
	}
	resp.Body.Close()
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); m != nil {
		*cleanup = append(*cleanup, "/documents/"+m[1]+"/delete")
	}
	after, _, _ := r.tiles("")
	num := func(m map[string]numbersTile) int {
		n, _ := strconv.Atoi(m["work/Daily reports filed"].Value)
		return n
	}
	if after == nil || num(after) != num(before)+1 {
		return fmt.Sprintf("daily reports filed = %d, want %d", num(after), num(before)+1)
	}
	list, err := r.client.Get(r.base + after["work/Daily reports filed"].Href)
	if err != nil {
		return err.Error()
	}
	body, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if !strings.Contains(string(body), "Acceptance numbers report "+stamp) {
		return "the daily reports tile links to a list that does not hold the report just filed"
	}
	return ""
}

// dealNumbers is the deals half of the check. It returns why it failed, or nothing.
func (r *runner) dealNumbers(me, stamp string, before map[string]numbersTile, cleanup *[]string) string {
	newDeal := func(name, amount, stage string) string {
		resp, err := r.client.PostForm(r.base+"/deals", url.Values{
			"name": {name}, "amount": {amount}, "stage": {stage}, "owner": {me},
		})
		if err != nil {
			return ""
		}
		resp.Body.Close()
		parts := strings.Split(strings.TrimSuffix(resp.Request.URL.Path, "/"), "/")
		if len(parts) == 3 && parts[1] == "deals" {
			*cleanup = append(*cleanup, "/deals/"+parts[2]+"/delete")
			return parts[2]
		}
		return ""
	}
	open := newDeal("Acceptance: open deal "+stamp, "1250", "proposal")
	won := newDeal("Acceptance: won deal "+stamp, "400", "new")
	if open == "" || won == "" {
		return "the deals were not created"
	}
	resp, err := r.client.PostForm(r.base+"/deals/"+won+"/move", url.Values{"stage": {"won"}})
	if err != nil {
		return err.Error()
	}
	resp.Body.Close()

	after, _, _ := r.tiles("")
	if after == nil {
		return "could not read the dashboard after the deals"
	}
	if got, want := moneyOf(after["deals/Open pipeline"].Value), moneyOf(before["deals/Open pipeline"].Value)+125000; got != want {
		return fmt.Sprintf("open pipeline = %d cents, want %d", got, want)
	}
	if got, want := moneyOf(after["deals/Won this month"].Value), moneyOf(before["deals/Won this month"].Value)+40000; got != want {
		return fmt.Sprintf("won this month = %d cents, want %d", got, want)
	}
	for _, c := range []struct{ tile, holds string }{
		{"deals/Open pipeline", "Acceptance: open deal " + stamp},
		{"deals/Won this month", "Acceptance: won deal " + stamp},
	} {
		resp, err := r.client.Get(r.base + after[c.tile].Href)
		if err != nil {
			return err.Error()
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), c.holds) {
			return fmt.Sprintf("%q links to %s, which does not hold %q", c.tile, after[c.tile].Href, c.holds)
		}
	}
	// A deal won this month does not show on the open pipeline's list.
	resp, err = r.client.Get(r.base + after["deals/Open pipeline"].Href)
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "Acceptance: won deal "+stamp) {
			return "the won deal is on the open pipeline's list"
		}
	}
	return ""
}
