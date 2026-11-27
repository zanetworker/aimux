package parts

import (
	"maps"
	"slices"
)

// Candidates is the names each edit of the inference proxy's settings would take right now, sorted. It is what a
// frontend offers to pick from, so a name is in a list only when the edit would not refuse it.
type Candidates struct {
	Enable  []string // engines EnableEngine would switch on
	Disable []string // engines DisableEngine would switch off
	Remove  []string // engines RemoveEngine would forget
	// Entries is the entries of models: what SetModel gives another model, and what SetRole gives a role.
	Entries []string
	// FreeEntries is the entries no role uses: what ClearModel takes out.
	FreeEntries []string
	Roles       []string // roles SetRole can give an entry
	SetRoles    []string // roles the file sets: what ClearRole takes out
}

// EditCandidates reads file and lists what each edit would take. A file that cannot be read, or that has a
// problem, gives none: every edit refuses such a file. It reads only.
func EditCandidates(file string) Candidates {
	settings, err := Load(file)
	if err != nil || len(settings.Validate()) > 0 {
		return Candidates{}
	}
	views, _, err := ListEngines(file)
	if err != nil {
		return Candidates{}
	}
	proxy := settings.Parts[proxyPart]
	// Switching an engine, naming a model and giving a role an entry all need the proxy on; taking out does not.
	_, offErr := proxyOn(settings)
	on := offErr == nil
	// An engine an entry of models still names is neither switched off nor forgotten.
	named := map[string]bool{}
	for _, model := range proxy.Models {
		named[ModelEngine(model)] = true
	}
	var c Candidates
	for _, v := range views {
		if v.Name == EngineCodex {
			continue
		}
		switch {
		case on && !v.On:
			c.Enable = append(c.Enable, v.Name)
		case on && !named[v.Name]:
			c.Disable = append(c.Disable, v.Name)
		}
		if v.Name != EngineAnthropic && !named[v.Name] {
			c.Remove = append(c.Remove, v.Name)
		}
	}
	slices.Sort(c.Enable)
	slices.Sort(c.Disable)
	slices.Sort(c.Remove)
	used := slices.Collect(maps.Values(proxy.Roles))
	entries := slices.Sorted(maps.Keys(proxy.Models))
	c.FreeEntries = slices.DeleteFunc(slices.Clone(entries), func(entry string) bool { return slices.Contains(used, entry) })
	c.SetRoles = slices.Sorted(maps.Keys(proxy.Roles))
	if on {
		c.Entries, c.Roles = entries, []string{RoleSubagent}
	}
	return c
}
