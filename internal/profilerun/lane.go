package profilerun

// AskRequest is one Wingman side-panel question as the lane engines take it.
type AskRequest struct {
	URL      string
	Title    string
	Page     string
	Question string
	Recap    string
	// Prior is the user's earlier questions on this page from another
	// mode, oldest first (no answers); see priorBlock.
	Prior     []string
	SessionID string
	Model     string

	// OriginOnly: the prompt names only the URL's scheme://host[:port]
	// (a private page asked about without its text).
	OriginOnly bool
}

// LaneEvent is one thing a lane engine streams back: Type is delta or
// tool_call.
type LaneEvent struct {
	Type string
	Text string
	Tool string
}
