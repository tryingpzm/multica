package views

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Text sanitization of views (SA src/app/views/text.ts).

var (
	htmlComment = regexp.MustCompile(`<!--[\s\S]*?-->`)
	htmlTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	entity      = regexp.MustCompile(`&(amp|lt|gt|quot|#39|nbsp);`)
	image       = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)|\[Image: [^\]]*\]`)
	mdLink      = regexp.MustCompile(`\[([^\]]+)\]\((?:[^)]*)\)`)
	heading     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	bullet      = regexp.MustCompile(`(?m)^\s*[-*]\s+`)
	emphasis    = regexp.MustCompile("(\\*\\*|__|`)")
	spaces      = regexp.MustCompile(`\s+`)
	blanks      = regexp.MustCompile(`[ \t]+`)
	paragraphs  = regexp.MustCompile(`\n{3,}`)
	labelled    = regexp.MustCompile(`^(?:([^\n{[]{1,200}): )?(\{[\s\S]*\}|\[[\s\S]*\])$`)
)

var entities = map[string]string{"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "#39": "'", "nbsp": " "}

func withoutHTML(text string) string {
	strip := func(s string) string { return htmlTag.ReplaceAllString(htmlComment.ReplaceAllString(s, ""), "") }
	text = strip(text)
	text = entity.ReplaceAllStringFunc(text, func(m string) string { return entities[m[1:len(m)-1]] })
	return strip(text)
}

func plain(text string, lines bool) string {
	out := image.ReplaceAllString(withoutHTML(text), "[image]")
	out = mdLink.ReplaceAllString(out, "$1")
	out = heading.ReplaceAllString(out, "")
	if lines {
		out = bullet.ReplaceAllString(out, "• ")
	} else {
		out = bullet.ReplaceAllString(out, "")
	}
	out = emphasis.ReplaceAllString(out, "")
	if lines {
		return strings.TrimSpace(paragraphs.ReplaceAllString(blanks.ReplaceAllString(out, " "), "\n\n"))
	}
	return strings.TrimSpace(spaces.ReplaceAllString(out, " "))
}

func markdown(text string) string {
	return strings.TrimSpace(image.ReplaceAllString(withoutHTML(text), "[image]"))
}

func scalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func inline(v any) string {
	if scalar(v) {
		if v == nil {
			return ""
		}
		data, _ := json.Marshal(v)
		if s, ok := v.(string); ok {
			return s
		}
		return string(data)
	}
	parts, sep := []string{}, " · "
	if list, ok := v.([]any); ok {
		sep = ", "
		for _, item := range list {
			if s := inline(item); s != "" {
				parts = append(parts, s)
			}
		}
	} else {
		m := v.(map[string]any)
		for _, k := range sortedKeys(m) {
			if s := inline(m[k]); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, sep)
}

func allScalar(list []any) bool {
	for _, item := range list {
		if !scalar(item) {
			return false
		}
	}
	return true
}

func jsonLines(v any) []string {
	if list, ok := v.([]any); ok && !allScalar(list) {
		out := []string{}
		for _, item := range list {
			out = append(out, "- "+inline(item))
		}
		return out
	}
	if m, ok := v.(map[string]any); ok {
		out := []string{}
		for _, k := range sortedKeys(m) {
			item := m[k]
			if list, isList := item.([]any); scalar(item) || (isList && allScalar(list)) {
				out = append(out, k+": "+inline(item))
			} else {
				out = append(append(out, k+":"), jsonLines(item)...)
			}
		}
		return out
	}
	return []string{inline(v)}
}

// readable formats a JSON payload, or a "label: <JSON>" notice, as key-value lines.
func readable(text string) string {
	m := labelled.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return text
	}
	var v any
	if err := json.Unmarshal([]byte(m[2]), &v); err != nil {
		return text
	}
	lines := jsonLines(v)
	if m[1] != "" {
		lines = append([]string{m[1]}, lines...)
	}
	return strings.Join(lines, "\n")
}

func preview(text string, length int) string {
	r := []rune(plain(readable(text), false))
	if len(r) > length {
		r = r[:length]
	}
	return string(r)
}

func attribute(v any) string {
	s, _ := v.(string)
	return s
}
