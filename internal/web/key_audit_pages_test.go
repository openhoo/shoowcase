package web

import (
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestKeyAndAuditDirectoriesPaginate(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "directories.sqlite"), "team-a", false)
	cookie, _ := joinMemberConsole(t, app)
	who := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	for i := 1; i <= 26; i++ {
		key, _, err := store.CreateKey(t.Context(), who, fmt.Sprintf("Agent %02d", i), "reader", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RevokeKey(t.Context(), who, key.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range []struct{ path, title, more string }{
		{"/keys", "your-keys", "Next keys →"},
		{"/admin?view=keys", "workspace-keys", "Next keys →"},
		{"/admin?view=activity", "activity", "Older events →"},
	} {
		t.Run(page.title, func(t *testing.T) {
			get := func(path string) string {
				out := hitRoute(t, app, "GET", testControl+path, cookie, nil)
				requireMemberStatus(t, out, 200)
				body := out.Body.String()
				if !strings.Contains(body, `role="table" aria-labelledby="`+page.title+`"`) {
					t.Fatal("missing directory table")
				}
				return body
			}
			body := get(page.path)
			href := regexp.MustCompile(`<a href="([^"]+)">` + regexp.QuoteMeta(page.more) + `</a>`).FindStringSubmatch(body)
			if len(href) != 2 || strings.Count(body, `<th scope="row"`) != 25 {
				t.Fatal("expected a full first page and a continuation link")
			}
			next := get(html.UnescapeString(href[1]))
			if page.title == "activity" {
				events := regexp.MustCompile(`class="site-address">#([0-9]+)<`)
				sequences := events.FindAllStringSubmatch(body, -1)
				older := events.FindAllStringSubmatch(next, -1)
				if len(sequences) != 25 || len(older) != 25 || strings.Contains(next, ">#"+sequences[24][1]+"<") {
					t.Fatal("audit continuation repeated events or lost the next page")
				}
			} else {
				if strings.Count(next, `<th scope="row"`) != 1 || strings.Contains(next, page.more) {
					t.Fatal("expected one remaining key and no continuation")
				}
				for i := 1; i <= 26; i++ {
					if strings.Count(body+next, fmt.Sprintf(">Agent %02d<", i)) != 1 {
						t.Fatal("key missing or duplicated between pages")
					}
				}
			}
		})
	}
}
