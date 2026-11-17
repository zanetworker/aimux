package httpsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// BuildURL is the request URL for one call: baseURL (https, with a host)
// plus pathTemplate with each {param} replaced by its value escaped as one
// path segment (url.PathEscape, so ? and # cannot leave it), and
// the query parameters given, encoded with url.Values. It refuses a key
// params lacks, a missing required parameter, a value of the wrong type or
// outside its enum, a string with a control character or a leading '-',
// and a path value that is empty, "." or "..", or holds '/', '\' or '%'. The result's host is
// checked against baseURL's.
func BuildURL(baseURL, pathTemplate string, in map[string]any, params []Param) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return "", errors.New("base URL is not an https URL with a host") // never echo it
	}
	byName := map[string]Param{}
	for _, p := range params {
		byName[p.Name] = p
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := byName[k]; !ok {
			return "", fmt.Errorf("unknown argument %q", k)
		}
	}
	text := map[string]string{}
	for _, p := range params {
		v, ok := in[p.Name]
		if !ok {
			if p.Required {
				return "", fmt.Errorf("missing required argument %q", p.Name)
			}
			continue
		}
		s, err := valueText(p, v)
		if err != nil {
			return "", fmt.Errorf("argument %q: %w", p.Name, err)
		}
		if p.In == "path" && (s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\%`)) {
			// An API that decodes %2F (or %25 twice) and resolves dot
			// segments could route an escaped value outside the approved
			// operation, on the same host.
			return "", fmt.Errorf("argument %q: %q is not one path segment (no /, \\ or %%, not empty, . or ..)", p.Name, s)
		}
		text[p.Name] = s
	}
	segs := strings.Split(pathTemplate, "/")
	for i, seg := range segs {
		if name, ok := wholeParam(seg); ok {
			s, ok := text[name]
			if !ok || byName[name].In != "path" {
				return "", fmt.Errorf("path parameter %q has no value", name)
			}
			segs[i] = url.PathEscape(s)
		}
	}
	query := url.Values{}
	for _, p := range params {
		if s, ok := text[p.Name]; ok && p.In == "query" {
			query.Set(p.Name, s)
		}
	}
	out := base.Scheme + "://" + base.Host + strings.TrimSuffix(base.EscapedPath(), "/") + strings.Join(segs, "/")
	if len(query) > 0 {
		out += "?" + query.Encode()
	}
	u, err := url.Parse(out)
	if err != nil || u.Host != base.Host || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return "", errors.New("the request URL left its base host")
	}
	return out, nil
}

// valueText is v (as decoded from JSON) as text for parameter p.
func valueText(p Param, v any) (string, error) {
	var s string
	switch p.Type {
	case "integer":
		n, ok := integer(v)
		if !ok {
			return "", errors.New("must be an integer")
		}
		s = strconv.FormatInt(n, 10)
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return "", errors.New("must be a boolean")
		}
		s = strconv.FormatBool(b)
	default:
		str, ok := v.(string)
		if !ok {
			return "", errors.New("must be a string")
		}
		if strings.HasPrefix(str, "-") {
			return "", errors.New("may not start with '-'")
		}
		if strings.IndexFunc(str, unicode.IsControl) >= 0 {
			return "", errors.New("may not hold control characters")
		}
		s = str
	}
	if len(p.Enum) > 0 && !slices.Contains(p.Enum, s) {
		return "", fmt.Errorf("must be one of %s", strings.Join(p.Enum, ", "))
	}
	return s, nil
}

// integer is v as an int64 when it is a whole number in range.
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
