package web

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

type zipFixtureEntry struct {
	path string
	body string
	mode fs.FileMode
}

func makeSiteZIP(t *testing.T, entries ...zipFixtureEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.path, Method: zip.Deflate}
		header.SetMode(e.mode)
		w, err := z.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSiteArchiveLayouts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []zipFixtureEntry
		paths string
	}{
		{"Finder folder", []zipFixtureEntry{
			{"Team handbook/", "", fs.ModeDir}, {"Team handbook/index.html", "<h1>Hello</h1>", 0},
			{"Team handbook/css/main.css", "h1{color:navy}", 0}, {"__MACOSX/._Team handbook", "metadata", 0},
			{"Team handbook/.DS_Store", "desktop metadata", 0},
		}, "index.html,css/main.css"},
		{"Assets only", []zipFixtureEntry{{"styles/a.css", "a{}", 0}}, "styles/a.css"},
		{"Multiple roots", []zipFixtureEntry{{"docs/index.html", "<p>Docs</p>", 0}, {"other.css", "p{}", 0}}, "docs/index.html,other.css"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, err := consoleZIP(makeSiteZIP(t, tc.files...))
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, file := range files {
				paths = append(paths, file.Path)
				b, err := base64.StdEncoding.DecodeString(file.Base64)
				if err != nil || len(b) == 0 {
					t.Fatal("ZIP source bytes missing", file.Path)
				}
			}
			if got := strings.Join(paths, ","); got != tc.paths {
				t.Fatalf("got %s, want %s", got, tc.paths)
			}
		})
	}
}

func TestSiteArchiveRejectsBrokenOrUnsupportedInput(t *testing.T) {
	check := func(name string, archive []byte) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			files, err := consoleZIP(archive)
			if err == nil || errorCode(err) != "invalid_argument" || len(files) != 0 {
				t.Fatalf("expected no files and useful input error; got %d files, %v", len(files), err)
			}
		})
	}
	var many []zipFixtureEntry
	for i := 0; i <= sites.MaxFiles; i++ {
		many = append(many, zipFixtureEntry{fmt.Sprintf("%03d.css", i), "p{}", 0})
	}
	check("too many files", makeSiteZIP(t, many...))
	for i := len(many); i <= maxArchiveEntries; i++ {
		many = append(many, zipFixtureEntry{fmt.Sprintf("%03d/", i), "", fs.ModeDir})
	}
	check("too many directory entries", makeSiteZIP(t, many...))
	check("text exceeds limit", makeSiteZIP(t, zipFixtureEntry{"a.css", strings.Repeat(" ", sites.MaxTextBytes+1), 0}))
	check("metadata counts toward size", makeSiteZIP(t, zipFixtureEntry{"index.html", "A", 0}, zipFixtureEntry{".DS_Store", strings.Repeat("x", sites.MaxRevisionBytes), 0}))
	check("dot component", makeSiteZIP(t, zipFixtureEntry{"data/../index.html", "p", 0}))
	check("rooted path", makeSiteZIP(t, zipFixtureEntry{"/index.html", "p", 0}))
	check("backslash", makeSiteZIP(t, zipFixtureEntry{`site\index.html`, "p", 0}))
	check("duplicate", makeSiteZIP(t, zipFixtureEntry{"a.css", "p{}", 0}, zipFixtureEntry{"a.css", "h1{}", 0}))
	check("symbolic link", makeSiteZIP(t, zipFixtureEntry{"a.css", "target", fs.ModeSymlink}))
	check("folder symbolic link", makeSiteZIP(t, zipFixtureEntry{"a/", "", fs.ModeSymlink}, zipFixtureEntry{"index.html", "p", 0}))
	check("unsupported media", makeSiteZIP(t, zipFixtureEntry{"notes.txt", "text", 0}))
	check("no site files", makeSiteZIP(t, zipFixtureEntry{"folder/", "", fs.ModeDir}))
	check("invalid archive", []byte("This is a note"))
	archive := makeSiteZIP(t, zipFixtureEntry{"a.css", "p{color:red}", 0})
	check("truncated", archive[:len(archive)-5])
	corrupt := bytes.Clone(archive)
	directory := bytes.LastIndex(corrupt, []byte{'P', 'K', 1, 2})
	if directory < 0 {
		t.Fatal("missing fixture directory")
	}
	corrupt[directory+16] ^= 1 // Inconsistent recorded checksum.
	check("integrity", corrupt)
}
