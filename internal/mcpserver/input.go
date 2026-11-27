package mcpserver

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The schema validator includes submitted values in its errors. After it
// rejects a call, find a correction using catalog metadata only. The valid
// path pays for a single schema validation; on errors the diagnostic walk
// never formats the submitted data or runs validation again.
type inputCheck func(value any, field string) *inputIssue
type inputIssue struct{ field, message string }

func compileDiagnostic(s *schema) inputCheck {
	names := slices.Sorted(maps.Keys(s.Properties))
	children := make(map[string]inputCheck, len(names))
	for _, name := range names {
		children[name] = compileDiagnostic(s.Properties[name])
	}
	var item inputCheck
	if s.Items != nil {
		item = compileDiagnostic(s.Items)
	}
	var pattern *regexp.Regexp
	if s.Pattern != "" {
		pattern = regexp.MustCompile(s.Pattern)
	}
	return func(value any, field string) *inputIssue {
		subfield := func(name string) string {
			if field == "" {
				return name
			}
			return field + "." + name
		}
		switch s.Type {
		case "object":
			obj, ok := value.(map[string]any)
			if !ok {
				return &inputIssue{field, "Use a JSON object."}
			}
			for _, name := range s.Required {
				if _, present := obj[name]; !present {
					return &inputIssue{subfield(name), "This field is required."}
				}
			}
			for name := range obj {
				if _, known := children[name]; !known {
					if len(names) == 0 {
						return &inputIssue{field, "This tool takes no arguments."}
					}
					return &inputIssue{field, "Remove unrecognized fields. Accepted fields: " + strings.Join(names, ", ") + "."}
				}
			}
			for _, name := range names {
				if v, present := obj[name]; present {
					if issue := children[name](v, subfield(name)); issue != nil {
						return issue
					}
				}
			}
			if len(s.OneOf) > 0 {
				// The upload file alternatives each require one encoding.
				matches := 0
				for _, variant := range s.OneOf {
					if len(variant.Required) != 1 {
						return nil // Leave unfamiliar constraints to the tool's hint.
					}
					if _, present := obj[variant.Required[0]]; present {
						matches++
					}
				}
				if matches != 1 {
					return &inputIssue{field, s.Description}
				}
			}
		case "array":
			items, ok := value.([]any)
			if !ok {
				return &inputIssue{field, "Use a JSON array."}
			}
			if s.MaxItems != nil && len(items) > *s.MaxItems {
				return &inputIssue{field, fmt.Sprintf("Supply at most %d items.", *s.MaxItems)}
			}
			for i, v := range items {
				if issue := item(v, fmt.Sprintf("%s[%d]", field, i)); issue != nil {
					return issue
				}
			}
		case "string":
			v, ok := value.(string)
			if !ok {
				return &inputIssue{field, "Use a JSON string."}
			}
			if len(s.Enum) != 0 {
				options := make([]string, len(s.Enum))
				for i, option := range s.Enum {
					if option == v {
						return nil
					}
					options[i] = fmt.Sprint(option)
				}
				return &inputIssue{field, "Choose one of: " + strings.Join(options, ", ") + "."}
			}
			if s.MinLength != nil && s.MaxLength != nil {
				if n := utf8.RuneCountInString(v); n < *s.MinLength || n > *s.MaxLength {
					if *s.MinLength == *s.MaxLength {
						return &inputIssue{field, fmt.Sprintf("Use exactly %d characters. %s", *s.MinLength, s.Description)}
					}
					return &inputIssue{field, fmt.Sprintf("Use %d to %d characters. %s", *s.MinLength, *s.MaxLength, s.Description)}
				}
			}
			if pattern != nil && !pattern.MatchString(v) {
				return &inputIssue{field, s.Description}
			}
		case "integer":
			if s.Minimum != nil && s.Maximum != nil {
				n, ok := value.(float64)
				if !ok || math.Trunc(n) != n || n < *s.Minimum || n > *s.Maximum {
					return &inputIssue{field, fmt.Sprintf("Use an integer from %g to %g.", *s.Minimum, *s.Maximum)}
				}
			}
		}
		return nil
	}
}
