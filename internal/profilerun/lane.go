package profilerun

// AskRequest is one Wingman side-panel question as the lane engines take it.
type AskRequest struct {
	URL       string
	Title     string
	Page      string
	Question  string
	Recap     string
	SessionID string
	Model     string
}

// LaneEvent is one thing a lane engine streams back: Type is delta or
// tool_call.
type LaneEvent struct {
	Type string
	Text string
	Tool string
}
