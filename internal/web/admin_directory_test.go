package web

import (
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
	"golang.org/x/net/html"
)

// Read names and links as a browser would, including query escaping.
func directoryPageLinks(t *testing.T, page string) (names []string, links map[string]string) {
	t.Helper()
	doc, err := html.ParseWithOptions(strings.NewReader(page), html.ParseOptionEnableScripting(false))
	if err != nil {
		t.Fatal(err)
	}
	links = make(map[string]string)
	for a := range doc.Descendants() {
		if a.Type != html.ElementNode || a.Data != "a" || a.FirstChild == nil || a.FirstChild.Type != html.TextNode {
			continue
		}
		name := a.FirstChild.Data
		for _, attr := range a.Attr {
			switch attr.Key {
			case "href":
				links[name] = attr.Val
			case "class":
				if attr.Val == "site-name" {
					names = append(names, name)
				}
			}
		}
	}
	return names, links
}

func TestMemberDirectoryAcrossPages(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "members.sqlite"), "team-a", false)
	cookie, _ := joinMemberConsole(t, app)
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	ctx := t.Context()
	for i := 0; i < 30; i++ {
		a := sites.Actor{Subject: fmt.Sprintf("user-%02d", 29-i), Workspace: admin.Workspace, Role: "editor"}
		if _, err := store.EnrollAccount(ctx, a, fmt.Sprintf("Team %02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	subject := "id&equals=Team"
	if err := store.ProvisionMember(ctx, admin, subject, "Zebra"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, admin, subject, "", true, "", false); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		resp := hitRoute(t, app, "GET", testControl+path, cookie, nil)
		requireMemberStatus(t, resp, 200)
		return resp.Body.String()
	}
	page := get("/admin?q=TEAM&role=editor&state=active")
	first, links := directoryPageLinks(t, page)
	if len(first) != 25 || first[0] != "Team 00" || first[24] != "Team 24" || !strings.Contains(page, "30 of 32 members") {
		t.Fatalf("unexpected first page or count: %q", first)
	}
	last, links := directoryPageLinks(t, get(links["Next members →"]))
	if !reflect.DeepEqual(last, []string{"Team 25", "Team 26", "Team 27", "Team 28", "Team 29"}) {
		t.Fatal("paging lost filter or order", last)
	}
	previous, _ := directoryPageLinks(t, get(links["← Previous members"]))
	if !reflect.DeepEqual(previous, first) {
		t.Fatal("previous page lost filter or sort", previous)
	}
	suspended, links := directoryPageLinks(t, get("/admin?q="+url.QueryEscape(subject)+"&role=reader&state=suspended"))
	if len(suspended) != 1 || suspended[0] != "Zebra" || !strings.Contains(get(links["Zebra"]), "Restore access") {
		t.Fatal("suspended member not found by exact sign-in ID or detail link broken", suspended)
	}
	bySubject, _ := directoryPageLinks(t, get("/admin?q=Team&role=editor&sort=subject"))
	if len(bySubject) != 25 || bySubject[0] != "Team 29" {
		t.Fatal("sign-in ID sort did not change order", bySubject)
	}
	missing := get("/admin?q=unmatched")
	if !strings.Contains(missing, "No matching members") || !strings.Contains(missing, "0 of 32 members") {
		t.Fatal("empty directory lost match context")
	}
	for _, query := range []string{"?q=" + strings.Repeat("a", 101), "?state=missing", "?role=missing", "?sort=invalid"} {
		requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/admin"+query, cookie, nil), 400)
	}
}

func TestGroupDirectoryResultsAndRoster(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "groups.sqlite"), "team-a", false)
	cookie, _ := joinMemberConsole(t, app)
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	get := func(path string) string {
		t.Helper()
		resp := hitRoute(t, app, "GET", testControl+path, cookie, nil)
		requireMemberStatus(t, resp, 200)
		return resp.Body.String()
	}
	ctx := t.Context()
	for _, group := range []struct{ name, role string }{{"Art team", "editor"}, {"Beta team", "reader"}, {"Text team", "editor"}} {
		g, err := store.CreateGroup(ctx, admin, group.name, group.role)
		if err != nil {
			t.Fatal(err)
		}
		if g.Name == "Text team" {
			if err := store.SetGroupMember(ctx, admin, g.ID, admin.Subject, true, g.Version); err != nil {
				t.Fatal(err)
			}
		}
	}
	body := get("/admin?view=groups&q=TEAM&role=editor&sort=members")
	names, links := directoryPageLinks(t, body)
	if !reflect.DeepEqual(names, []string{"Text team", "Art team"}) || !strings.Contains(body, "2 of 3 groups") {
		t.Fatal("group filtering or count sort wrong", names)
	}
	roster := get(links["Text team"])
	if !strings.Contains(roster, "Group members") || !strings.Contains(roster, "Remove tester from group") || !strings.Contains(roster, "member=tester") {
		t.Fatal("group roster missing member or controls")
	}
	if !strings.Contains(get("/admin?view=groups&q=missing"), "No matching groups") {
		t.Fatal("empty search indistinguishable from no groups")
	}
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/admin?view=groups&role=admin", cookie, nil), 400)
}
