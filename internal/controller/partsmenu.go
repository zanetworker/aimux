package controller

import "github.com/zanetworker/aimux/internal/parts"

// What a part's line on the setup menu says it is.
const (
	PartOn           = "on"
	PartOff          = "off"
	PartNotSet       = "not set" // the file does not name it: aimux neither starts nor stops it, and it may be running
	PartNotAvailable = "not available yet"
)

// PartChoice is one line of the setup menu: a part, whether it is on, and whether it can be turned on from there.
type PartChoice struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
	State string `json:"state"`
	// Number is what the owner types to turn it on; 0 for a part that is not offered.
	Number int `json:"-"`
	// Why says why a part is not offered, when that is not plain from its state.
	Why string `json:"why,omitempty"`
}

// PartsMenu is every part in the registry's order, with what the settings say of it. A part is offered when it is
// not on and aimux starts a service of its own for it.
func PartsMenu(s parts.Settings) []PartChoice {
	var menu []PartChoice
	next := 1
	for _, p := range parts.Parts() {
		c := PartChoice{ID: p.ID, Name: p.Name, About: p.About, State: PartNotSet}
		if stated, ok := s.Parts[p.ID]; ok && stated.Enabled != nil {
			c.State = PartOff
			if *stated.Enabled {
				c.State = PartOn
			}
		}
		switch {
		case p.Always:
			c.State, c.Why = PartOn, "always on"
		case !p.Available:
			c.State = PartNotAvailable
		case len(p.Services) == 0:
			c.Why = "aimux starts nothing for it yet"
		case !managedServices(p):
			c.Why = "its tool gateway is managed by Wingman"
		case c.State != PartOn:
			c.Number = next
			next++
		}
		menu = append(menu, c)
	}
	return menu
}

// managedServices reports whether every service behind p is one aimux manages.
func managedServices(p parts.Part) bool {
	for _, id := range p.Services {
		if !parts.MustService(id).Managed {
			return false
		}
	}
	return true
}
