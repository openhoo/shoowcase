package webui

import (
	_ "embed"
	"html/template"
)

//go:embed action-icons.html
var nativeIcons string

// NativeTemplate adds the same Lucide artwork to script-free forms as React.
// Each named template is static SVG generated from the pinned UI dependency.
func NativeTemplate(name string) *template.Template {
	return template.Must(template.New(name).Parse(nativeIcons))
}
