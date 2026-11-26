package parts

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The changes a command makes to the parts section: an engine switched on or off, added or forgotten, a part
// switched on or off. Each reads the file, refuses when it has a problem already, makes its edits to the text,
// and writes only if the result is a section without problems that says what was asked.

const proxyPart = "inference-proxy"

// step is one change to the file's text.
type step struct {
	path  []string
	value any
}

// change applies the edits that plan decides on from the settings as they are.
func change(file string, plan func(s Settings) ([]step, error)) error {
	return update(file, func(data []byte) ([]byte, error) {
		s, err := Parse(data)
		if err != nil {
			return data, err
		}
		if problems := s.Validate(); len(problems) > 0 {
			return data, fmt.Errorf("the parts section has a problem to fix first: %s", problems[0])
		}
		edits, err := plan(s)
		if err != nil {
			return data, err
		}
		out := data
		for _, e := range edits {
			if out, err = Edit(out, e.path, e.value); err != nil {
				return data, err
			}
		}
		after, err := Parse(out)
		if err != nil {
			return data, err
		}
		if problems := after.Validate(); len(problems) > 0 {
			return data, fmt.Errorf("that would leave the parts section with a problem: %s", problems[0])
		}
		return out, nil
	})
}

func enginePath(name string, more ...string) []string {
	return append([]string{"parts", proxyPart, "engines", name}, more...)
}

// proxyOn is the edits that list the inference proxy as on, when the file does not list it. An engine belongs
// to the proxy, so stating one takes the proxy under management; it is running already, and that costs nothing.
func proxyOn(s Settings) ([]step, error) {
	proxy, listed := s.Parts[proxyPart]
	switch {
	case !listed:
		return []step{{[]string{"parts", proxyPart, "enabled"}, true}}, nil
	case proxy.Enabled != nil && !*proxy.Enabled:
		return nil, errors.New("the inference proxy is turned off in the file; turn it on first with: aimux enable inference-proxy")
	}
	return nil, nil
}

func checkEngineName(name string) error {
	if !engineName.MatchString(name) {
		return errors.New("that cannot be an engine's name: it is typed before the colon of a model name, so lower-case letters, digits and hyphens, starting with a letter")
	}
	return nil
}

// EnableEngine switches an engine on: Anthropic, which is built in, or a named upstream that was added before.
func EnableEngine(file, name string) error {
	if err := checkEngineName(name); err != nil {
		return err
	}
	if name == EngineCodex {
		return errors.New("codex is built into the proxy and always on; there is nothing to enable")
	}
	return change(file, func(s Settings) ([]step, error) {
		edits, err := proxyOn(s)
		if err != nil {
			return nil, err
		}
		stated, listed := s.Parts[proxyPart].Engines[name]
		switch {
		case !listed && name == EngineAnthropic:
			return append(edits, step{enginePath(name), Block{{"enabled", true}}}), nil
		case !listed:
			return nil, fmt.Errorf("no engine called %s has been added; add it with: aimux engine add %s=<address>", name, name)
		case stated.Enabled != nil && !*stated.Enabled:
			return append(edits, step{enginePath(name, "enabled"), true}), nil
		}
		return edits, nil // listed, and on already
	})
}

// DisableEngine switches an engine off and keeps what the file says about it, so it can be switched on again.
func DisableEngine(file, name string) error {
	if err := checkEngineName(name); err != nil {
		return err
	}
	if name == EngineCodex {
		return errors.New("codex is built into the proxy and cannot be turned off")
	}
	return change(file, func(s Settings) ([]step, error) {
		edits, err := proxyOn(s)
		if err != nil {
			return nil, err
		}
		_, listed := s.Parts[proxyPart].Engines[name]
		switch {
		case !listed && name == EngineAnthropic:
			// Said outright, so it is off whatever the job was started with.
			return append(edits, step{enginePath(name), Block{{"enabled", false}}}), nil
		case !listed:
			return nil, fmt.Errorf("no engine called %s has been added", name)
		}
		return append(edits, step{enginePath(name, "enabled"), false}), nil
	})
}

// AddEngine tells aimux of a named upstream at address, a Messages API paid for with the owner's key, and
// switches it on. Its key is expected in the secret-store entry <name>-key, which is written out in the file so
// that renaming the engine later does not change which key it uses.
func AddEngine(file, name, address string) error {
	if err := checkEngineName(name); err != nil {
		return err
	}
	if scalar(name) != name {
		// true, null, yes and their like: written as a new key, a config file would read the name as a switch or
		// as nothing. One the owner wrote in quotes himself can still be switched and removed.
		return fmt.Errorf("%q cannot be a new engine's name: a config file reads it as something other than a name", name)
	}
	switch name {
	case EngineCodex:
		return errors.New("codex is built in; it is not added")
	case EngineAnthropic:
		return errors.New("anthropic is built in; it is not added, only turned on: aimux engine enable anthropic")
	}
	if why := checkOrigin(address); why != "" {
		return errors.New(why)
	}
	return change(file, func(s Settings) ([]step, error) {
		edits, err := proxyOn(s)
		if err != nil {
			return nil, err
		}
		if _, listed := s.Parts[proxyPart].Engines[name]; listed {
			return nil, fmt.Errorf("an engine called %s is in the file already; remove it first, or change its url there", name)
		}
		return append(edits, step{enginePath(name), Block{
			{"type", TypeMessages}, {"url", address}, {"key", Flow{{"keychain", Flow{{"item", name + "-key"}}}}},
		}}), nil
	})
}

// RemoveEngine forgets a named upstream, or Anthropic's entry.
func RemoveEngine(file, name string) error {
	if err := checkEngineName(name); err != nil {
		return err
	}
	if name == EngineCodex {
		return errors.New("codex is built into the proxy and cannot be removed")
	}
	return change(file, func(s Settings) ([]step, error) {
		if _, listed := s.Parts[proxyPart].Engines[name]; !listed {
			return nil, fmt.Errorf("no engine called %s is in the file", name)
		}
		return []step{{enginePath(name), nil}}, nil
	})
}

func partSwitch(file, id string, on bool) error {
	part, ok := PartByID(id)
	switch {
	case !ok:
		return fmt.Errorf("no part is called %s; the parts are: %s", plain(id), strings.Join(partIDs(), ", "))
	case part.Always:
		return fmt.Errorf("the %s is always on; there is nothing to switch", strings.ToLower(part.Name))
	case on && !part.Available:
		return fmt.Errorf("%s is not available yet", part.Name)
	}
	return change(file, func(Settings) ([]step, error) {
		return []step{{[]string{"parts", id, "enabled"}, on}}, nil
	})
}

// EnablePart states that a part is on.
func EnablePart(file, id string) error { return partSwitch(file, id, true) }

// DisablePart states that a part is off.
func DisablePart(file, id string) error { return partSwitch(file, id, false) }

// ListEngines is the inference proxy's engines as the file states them: the two that are built in, then the
// owner's by name. Codex is always on; Anthropic is on only when the file says so. stated is false when the file
// says nothing about engines, which means "as the proxy is installed": the views are then no statement at all.
// It reads the file and changes nothing.
func ListEngines(file string) (views []EngineView, stated bool, err error) {
	s, err := Load(file)
	if err != nil {
		return nil, false, err
	}
	if problems := s.Validate(); len(problems) > 0 {
		return nil, false, fmt.Errorf("the parts section has a problem: %s", problems[0])
	}
	proxy := s.Parts[proxyPart]
	names := make([]string, 0, len(proxy.Engines))
	for name := range proxy.Engines {
		if name != EngineCodex && name != EngineAnthropic {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	views = []EngineView{proxy.Engine(EngineCodex), proxy.Engine(EngineAnthropic)}
	for _, name := range names {
		views = append(views, proxy.Engine(name))
	}
	return views, proxy.Engines != nil, nil
}

func modelPath(entry string) []string { return []string{"parts", proxyPart, "models", entry} }
func rolePath(role string) []string   { return []string{"parts", proxyPart, "roles", role} }

// SetModel names a model: the entry is added to models, or given another model when it is there already. A role
// that names the entry follows it, which is how a new model is one changed line.
func SetModel(file, entry, model string) error {
	if !engineName.MatchString(entry) || scalar(entry) != entry {
		return errors.New("an entry is named in lower-case letters, digits and hyphens, starting with a letter")
	}
	return change(file, func(s Settings) ([]step, error) {
		edits, err := proxyOn(s)
		if err != nil {
			return nil, err
		}
		return append(edits, step{modelPath(entry), model}), nil
	})
}

// ClearModel takes an entry out of models. One that a role still names is refused, with the role.
func ClearModel(file, entry string) error {
	return change(file, func(s Settings) ([]step, error) {
		proxy := s.Parts[proxyPart]
		if _, listed := proxy.Models[entry]; !listed {
			return nil, fmt.Errorf("models has no entry called %s", plain(entry))
		}
		for _, role := range slices.Sorted(maps.Keys(proxy.Roles)) {
			if proxy.Roles[role] == entry {
				return nil, fmt.Errorf("the role %s uses this entry; clear the role first, or give it another entry", role)
			}
		}
		return []step{{modelPath(entry), nil}}, nil
	})
}

// SetRole says which entry of models a role uses. The entry has to be there: a role never names a model itself.
func SetRole(file, role, entry string) error {
	return change(file, func(s Settings) ([]step, error) {
		edits, err := proxyOn(s)
		if err != nil {
			return nil, err
		}
		return append(edits, step{rolePath(role), entry}), nil
	})
}

// ClearRole takes a role out of the file, so that kind of work uses the session's model again.
func ClearRole(file, role string) error {
	return change(file, func(s Settings) ([]step, error) {
		if _, set := s.Parts[proxyPart].Roles[role]; !set {
			return nil, fmt.Errorf("the role %s is not set in the file", plain(role))
		}
		return []step{{rolePath(role), nil}}, nil
	})
}

// ModelEngine is the engine that answers a model named as a session names it: the one in front of the first
// colon, or Anthropic for a name with none. A Claude alias (opus, sonnet, haiku) is Anthropic's too.
func ModelEngine(model string) string {
	if prefix, _, ok := strings.Cut(model, ":"); ok && engineName.MatchString(prefix) {
		return prefix
	}
	return EngineAnthropic
}
