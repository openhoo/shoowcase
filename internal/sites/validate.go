package sites

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	MaxFiles         = 100
	MaxFileBytes     = 2 << 20
	MaxTextBytes     = 256 << 10
	MaxRevisionBytes = 8 << 20
	MaxRequestBytes  = 12 << 20
)

var fileRE = regexp.MustCompile("^[A-Za-z0-9_-][A-Za-z0-9_.-]*$")

func validPath(p string) bool {
	if len(p) == 0 || len(p) > 200 || p != path.Clean(p) || strings.EqualFold(strings.SplitN(p, "/", 2)[0], "auth") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if !fileRE.MatchString(part) {
			return false
		}
	}
	return true
}

type fileInput struct {
	Path   string  `json:"path"`
	Text   *string `json:"text"`
	Base64 *string `json:"base64"`
}
type asset struct {
	Path        string `json:"path"`
	Digest      string `json:"digest"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
	data        []byte
}

// UploadTarget validates what is knowable before an upload: its logical path,
// supported media type, and declared size. The caller must still validate bytes.
func UploadTarget(p string, size int64) (string, error) {
	if !validPath(p) {
		return "", bad("Use a relative file path of 1–200 ASCII characters without hidden components; the top-level auth path is reserved.")
	}
	var ct string
	switch strings.ToLower(path.Ext(p)) {
	case ".html":
		ct = "text/html; charset=utf-8"
	case ".css":
		ct = "text/css; charset=utf-8"
	case ".gif":
		ct = "image/gif"
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	case ".png":
		ct = "image/png"
	default:
		return "", bad("Supported file types: HTML, CSS, PNG, JPEG, GIF.")
	}
	if size < 0 || size > MaxFileBytes {
		return "", bad("File exceeds 2 MiB or has an invalid size.")
	}
	if strings.HasPrefix(ct, "text/") && size > MaxTextBytes {
		return "", bad("HTML and CSS must be no larger than 256 KiB.")
	}
	return ct, nil
}

func prepare(f fileInput) (asset, error) {
	var a asset
	if (f.Text == nil) == (f.Base64 == nil) {
		return a, bad("Each file needs exactly one of text or base64.")
	}
	var data []byte
	if f.Text != nil {
		if len(*f.Text) > MaxFileBytes {
			return a, bad("File exceeds 2 MiB.")
		}
		data = []byte(*f.Text)
	} else {
		if len(*f.Base64) > base64.StdEncoding.EncodedLen(MaxFileBytes) {
			return a, bad("File exceeds 2 MiB.")
		}
		var e error
		data, e = base64.StdEncoding.Strict().DecodeString(*f.Base64)
		if e != nil {
			return a, bad("File has invalid standard base64.")
		}
	}
	ct, e := UploadTarget(f.Path, int64(len(data)))
	if e != nil {
		return a, e
	}
	if strings.HasPrefix(ct, "text/") {
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return a, bad("HTML and CSS must be UTF-8 text no larger than 256 KiB.")
		}
		var e error
		if strings.HasPrefix(ct, "text/html") {
			e = checkHTML(data)
		} else {
			e = checkCSS(string(data))
		}
		if e != nil {
			return a, e
		}
	} else {
		cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
		if e != nil || "image/"+format != ct || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 4_194_304 {
			return a, bad("Invalid raster image, MIME mismatch, or image exceeds 4 megapixels.")
		}
		if _, _, e = image.Decode(bytes.NewReader(data)); e != nil {
			return a, bad("Raster image cannot be decoded.")
		}
	}
	return asset{Path: f.Path, Digest: sum(data), ContentType: ct, Size: len(data), data: data}, nil
}
func wordset(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var tags = wordset("html head title meta link style body main nav header footer section article aside div span p h1 h2 h3 h4 h5 h6 ul ol li dl dt dd a img figure figcaption blockquote q hr br strong em b i u s small sub sup mark abbr code pre kbd samp var time address table caption thead tbody tfoot tr th td col colgroup details summary bdi bdo wbr picture")
var globals = wordset("id class lang dir role title hidden tabindex style")
var functionRE = regexp.MustCompile("([a-zA-Z_-][a-zA-Z0-9_-]*)[[:space:]]*\\(")
var atRE = regexp.MustCompile("@([a-zA-Z-]+)")
var cssFuncs = wordset("var calc min max clamp rgb rgba hsl hsla hwb oklab oklch lab lch color color-mix linear-gradient radial-gradient conic-gradient repeating-linear-gradient repeating-radial-gradient repeating-conic-gradient repeat minmax fit-content translate translatex translatey translate3d rotate scale scalex scaley scale3d skew skewx skewy matrix cubic-bezier steps nth-child nth-last-child nth-of-type nth-last-of-type not is where has lang blur brightness contrast grayscale hue-rotate invert opacity saturate sepia drop-shadow counter counters polygon circle ellipse inset rect round mod rem abs sign light-dark")

func checkCSS(css string) error {
	// The CSS profile deliberately excludes string-based resource functions, escapes,
	// comments and external rules. A function allowlist also covers nested functions.
	if strings.ContainsAny(css, "\\<>") || strings.Contains(css, "/*") || strings.Contains(css, "*/") {
		return bad("CSS escapes, markup, and comments are outside the supported static CSS profile.")
	}
	input := strings.ToLower(css)
	for _, r := range atRE.FindAllStringSubmatch(input, -1) {
		switch r[1] {
		case "media", "supports", "keyframes", "container", "layer":
		default:
			return bad("Unsupported CSS at-rule: @" + r[1])
		}
	}
	for _, f := range functionRE.FindAllStringSubmatch(input, -1) {
		if !cssFuncs[f[1]] && f[1] != "and" && f[1] != "or" && f[1] != "media" && f[1] != "supports" && f[1] != "container" {
			return bad("Unsupported CSS function: " + f[1])
		}
	}
	for _, p := range []string{"-moz-binding", "behavior:", "javascript:", "data:"} {
		if strings.Contains(input, p) {
			return bad("Unsupported active CSS.")
		}
	}
	return nil
}
func safeRef(s string, external bool) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\") || strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	u, e := url.Parse(s)
	if e != nil || u.User != nil {
		return false
	}
	if u.Scheme != "" {
		return external && strings.EqualFold(u.Scheme, "https") && u.Hostname() != ""
	}
	if u.Host != "" || strings.Contains(u.Path, "\\") || strings.HasPrefix(u.Path, "//") || strings.ContainsFunc(u.Path, unicode.IsControl) {
		return false
	}
	return true
}
func checkHTML(data []byte) error {
	doc, e := html.Parse(bytes.NewReader(data))
	if e != nil {
		return bad("Invalid HTML.")
	}
	todo := []*html.Node{doc}
	nodes := 0
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		nodes++
		if nodes > 24000 {
			return bad("HTML tree exceeds 24,000 nodes.")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			todo = append(todo, c)
		}
		if n.Type != html.ElementNode {
			continue
		}
		tag := n.Data
		if n.Namespace != "" || !tags[tag] {
			return bad("Unsupported HTML element: " + tag)
		}
		attrs := map[string]string{}
		for _, a := range n.Attr {
			key := a.Key
			if _, exists := attrs[key]; exists {
				return bad("Duplicate HTML attribute.")
			}
			attrs[key] = a.Val
			if a.Namespace != "" {
				return bad("HTML namespaces are unsupported.")
			}
			if key == "style" {
				if e := checkCSS(a.Val); e != nil {
					return e
				}
				continue
			}
			if globals[key] || strings.HasPrefix(key, "aria-") || strings.HasPrefix(key, "data-") {
				continue
			}
			allow := false
			switch tag {
			case "a":
				allow = key == "rel" || key == "href" && safeRef(a.Val, true)
			case "img":
				allow = key == "alt" || key == "height" || key == "width" || key == "loading" || key == "decoding" || key == "src" && safeRef(a.Val, false)
			case "link":
				allow = key == "rel" || key == "href" && safeRef(a.Val, false) || key == "media"
			case "meta":
				allow = key == "charset" || key == "name" || key == "content"
			case "td", "th":
				allow = key == "colspan" || key == "rowspan" || key == "scope" || key == "headers"
			case "col", "colgroup":
				allow = key == "span"
			case "details":
				allow = key == "open"
			case "time":
				allow = key == "datetime"
			case "ol":
				allow = key == "start" || key == "reversed" || key == "type"
			case "li":
				allow = key == "value"
			}
			if !allow {
				return bad("Unsupported attribute or URL: " + tag + "[" + key + "].")
			}
		}
		switch tag {
		case "meta":
			if v, ok := attrs["charset"]; ok && !strings.EqualFold(v, "utf-8") {
				return bad("Only UTF-8 charset is supported.")
			}
			if v, ok := attrs["name"]; ok && !wordset("viewport description author theme-color color-scheme robots")[strings.ToLower(v)] {
				return bad("Unsupported metadata name.")
			}
		case "link":
			if !strings.EqualFold(strings.TrimSpace(attrs["rel"]), "stylesheet") {
				return bad("Only same-site stylesheets can be linked.")
			}
		case "style":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.TextNode {
					return bad("Invalid style node.")
				}
				if e := checkCSS(c.Data); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
