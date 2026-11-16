package clipack

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// command returns the named command.
func (p Pack) command(name string) (Command, error) {
	for _, c := range p.Commands {
		if c.Name == name {
			return c, nil
		}
	}
	return Command{}, fmt.Errorf("unknown command %q in CLI pack %q", name, p.Name)
}

// Argv checks in against the command's flags and args and returns the
// command line, binary first: argv, flags in sorted key order, fixed, then
// "--" and the positional args. Any key the command does not declare, a
// missing required value, a wrong type, a value out of bounds or off its
// pattern, a flag-like value or a control character refuses the call.
func (p Pack) Argv(command string, in map[string]any) ([]string, error) {
	c, err := p.command(command)
	if err != nil {
		return nil, err
	}
	args := map[string]Flag{}
	for _, a := range c.Args {
		args[a.Name] = a
	}
	for k := range in {
		if _, ok := c.Flags[k]; ok {
			continue
		}
		if _, ok := args[k]; ok {
			continue
		}
		return nil, fmt.Errorf("%s: unknown flag %q", command, k)
	}

	out := append([]string{p.Binary}, c.Argv...)
	keys := make([]string, 0, len(c.Flags))
	for k := range c.Flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := c.Flags[k]
		v, ok := in[k]
		if !ok {
			if f.Required {
				return nil, fmt.Errorf("%s: missing required flag %q", command, k)
			}
			continue
		}
		s, err := f.value(v)
		if err != nil {
			return nil, fmt.Errorf("%s: flag %q: %w", command, k, err)
		}
		switch {
		case f.Type != "bool":
			out = append(out, "--"+k, s)
		case s == "true":
			out = append(out, "--"+k)
		}
	}
	out = append(out, c.Fixed...)

	var pos []string
	for _, a := range c.Args {
		v, ok := in[a.Name]
		if !ok {
			if a.Required {
				return nil, fmt.Errorf("%s: missing required arg %q", command, a.Name)
			}
			continue
		}
		s, err := a.value(v)
		if err != nil {
			return nil, fmt.Errorf("%s: arg %q: %w", command, a.Name, err)
		}
		pos = append(pos, s)
	}
	if len(pos) > 0 {
		out = append(append(out, "--"), pos...)
	}
	return out, nil
}

// value checks v against f and returns it as an argv word ("true" or
// "false" for bool).
func (f Flag) value(v any) (string, error) {
	switch f.Type {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("want a boolean, got %T", v)
		}
		return strconv.FormatBool(b), nil
	case "int":
		n, err := toInt(v)
		if err != nil {
			return "", err
		}
		if n < int64(f.Min) || n > int64(f.Max) {
			return "", fmt.Errorf("%d is outside %d to %d", n, f.Min, f.Max)
		}
		return strconv.FormatInt(n, 10), nil
	case "enum":
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("want one of %s, got %T", strings.Join(f.Values, ", "), v)
		}
		for _, allowed := range f.Values {
			if s == allowed {
				return s, nil
			}
		}
		return "", fmt.Errorf("%q is not one of %s", s, strings.Join(f.Values, ", "))
	case "string":
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("want a string, got %T", v)
		}
		if err := checkString(s); err != nil {
			return "", err
		}
		// Load compiles the pattern; a Flag that did not come through Load has none.
		if f.re == nil || !f.re.MatchString(s) {
			return "", fmt.Errorf("%q does not match %s", s, f.anchored())
		}
		return s, nil
	}
	return "", fmt.Errorf("type %q is not supported", f.Type)
}

// toInt accepts a whole number as JSON decoding or a Go caller gives it;
// a string, bool or fraction is refused.
func toInt(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) || n < math.MinInt32 || n > math.MaxInt32 {
			return 0, fmt.Errorf("%v is not a whole number in range", n)
		}
		return int64(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("%q is not a whole number", n.String())
		}
		return i, nil
	}
	return 0, fmt.Errorf("want an integer, got %T", v)
}

// checkString refuses a value the CLI could read as a flag, and any value
// with invalid UTF-8, control or invisible formatting characters, whatever
// its pattern says.
func checkString(s string) error {
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q looks like a flag", s)
	}
	return checkControl(s)
}

func checkControl(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("value is not valid UTF-8")
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("value has control character %U", r)
		}
	}
	return nil
}
