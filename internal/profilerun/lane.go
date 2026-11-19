package profilerun

// AskRequest is one Wingman side-panel question as the lane engines take it.
type AskRequest struct {
	URL      string
	Title    string
	Page     string
	Question string
	// Prior is the user's earlier questions on this page from another
	// mode, oldest first (no answers); see priorBlock.
	Prior []string
	// Image: an image is attached to the question; the prompt frames it as
	// untrusted data (imageFraming). The image itself is sent separately.
	Image     bool
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
	// Input and Failed are set on "tool_result" events only (the tool's
	// JSON input; whether the result was an error). Those events stay in
	// aimux: the sandbox engine hands them to the turn's collector
	// (WithToolResults) and never to a client.
	Input  string
	Failed bool
}
