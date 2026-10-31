package panel

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type msgPattern struct {
	re *regexp.Regexp
	tr [2]string
}

var (
	catalog   = map[string][2]string{}
	patterns  []msgPattern
	verbRe    = regexp.MustCompile(`%[sd]`)
	outVerbRe = regexp.MustCompile(`%(?:\[([1-9])\])?[sd]`)
)

func loadCatalog(assets fs.FS) {
	b, err := fs.ReadFile(assets, "static/i18n.json")
	if err != nil {
		return
	}
	var raw map[string][]string
	if json.Unmarshal(b, &raw) == nil {
		setCatalog(raw)
	}
}

func setCatalog(raw map[string][]string) {
	catalog = map[string][2]string{}
	patterns = nil
	for k, v := range raw {
		if len(v) < 2 {
			continue
		}
		catalog[k] = [2]string{v[0], v[1]}
		verbs := verbRe.FindAllString(k, -1)
		if len(verbs) == 0 {
			continue
		}
		var sb strings.Builder
		sb.WriteString("^")
		for i, part := range verbRe.Split(k, -1) {
			sb.WriteString(regexp.QuoteMeta(part))
			if i < len(verbs) {
				if verbs[i] == "%d" {
					sb.WriteString(`(-?\d+)`)
				} else {
					sb.WriteString(`(.+?)`)
				}
			}
		}
		sb.WriteString("$")
		patterns = append(patterns, msgPattern{regexp.MustCompile(sb.String()), catalog[k]})
	}
	sort.Slice(patterns, func(i, j int) bool { return len(patterns[i].re.String()) > len(patterns[j].re.String()) })
}

func requestLang(r *http.Request) string {
	switch l := r.Header.Get("X-Lang"); l {
	case "zh", "ja", "en":
		return l
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		switch {
		case strings.HasPrefix(p, "zh"):
			return "zh"
		case strings.HasPrefix(p, "ja"):
			return "ja"
		case strings.HasPrefix(p, "en"):
			return "en"
		}
	}
	return "zh"
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func tr(lang, s string) string {
	if lang != "ja" && lang != "en" || !hasHan(s) {
		return s
	}
	if strings.Contains(s, "\n") {
		lines := strings.Split(s, "\n")
		for i := range lines {
			lines[i] = tr(lang, lines[i])
		}
		return strings.Join(lines, "\n")
	}
	idx := 0
	if lang == "en" {
		idx = 1
	}
	if v, ok := catalog[s]; ok && v[idx] != "" {
		return v[idx]
	}
	for _, p := range patterns {
		m := p.re.FindStringSubmatch(s)
		if m == nil || p.tr[idx] == "" {
			continue
		}
		args, i := m[1:], 0
		return outVerbRe.ReplaceAllStringFunc(p.tr[idx], func(v string) string {
			n := i
			if sm := outVerbRe.FindStringSubmatch(v); sm[1] != "" {
				n = int(sm[1][0]-'0') - 1
			} else {
				i++
			}
			if n < 0 || n >= len(args) {
				return ""
			}
			return trList(lang, args[n])
		})
	}
	return s
}

func trList(lang, s string) string {
	if v := tr(lang, s); v != s || !strings.Contains(s, "、") {
		return v
	}
	parts := strings.Split(s, "、")
	for i := range parts {
		parts[i] = tr(lang, parts[i])
	}
	if lang == "en" {
		return strings.Join(parts, ", ")
	}
	return strings.Join(parts, "、")
}

func localize(lang string, v any) any {
	if lang != "ja" && lang != "en" {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if dec.Decode(&out) != nil {
		return v
	}
	return walk(lang, out)
}

func walk(lang string, v any) any {
	switch x := v.(type) {
	case string:
		return tr(lang, x)
	case map[string]any:
		for k, e := range x {
			x[k] = walk(lang, e)
		}
	case []any:
		for i, e := range x {
			x[i] = walk(lang, e)
		}
	}
	return v
}
