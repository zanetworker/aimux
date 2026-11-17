package browserhost

import "time"

// Request is one message from the Wingman side panel.
// Type is ask | route | history.list | history.get | history.delete | cancel.
type Request struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	TabID    int    `json:"tabId,omitempty"`
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
	Lane     string `json:"lane,omitempty"`
	Page     string `json:"page,omitempty"`
	Question string `json:"question,omitempty"`
	Query    string `json:"query,omitempty"`
	Model    string `json:"model,omitempty"`
	// Prior is the page's earlier questions from another mode (questions
	// only), sent on the first ask after a mode change.
	Prior []string `json:"prior,omitempty"`
	// Instance is the panel's browser profile id, so each browser keeps its
	// own conversations; a malformed one is ignored (legacy behaviour).
	Instance string `json:"instance,omitempty"`
	// OriginOnly: Research on a private page without its text; the prompt
	// names only the URL's origin.
	OriginOnly bool `json:"originOnly,omitempty"`
	// Text and Facts are a route request: the question and the three facts
	// about the page, all that POST /v1/route is sent.
	Text  string      `json:"text,omitempty"`
	Facts *RouteFacts `json:"facts,omitempty"`
	// Routed is an ask's route answer it followed (POST /v1/sessions routed).
	Routed *Routed `json:"routed,omitempty"`
	// Image is one image pasted into an ask's question, forwarded to POST
	// /v1/sessions as is (the server checks it).
	Image *Image `json:"image,omitempty"`
}

// Image is an image attached to a question: its mime type and base64 bytes
// (no data: prefix).
type Image struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// RouteFacts are what the panel knows about the page for Auto mode.
type RouteFacts struct {
	SitePrivate  bool `json:"sitePrivate"`
	PageReadable bool `json:"pageReadable"`
	UsePage      bool `json:"usePage"`
}

// Routed is the route answer an ask followed, with the mode it finally
// asked in (page, web or workspace).
type Routed struct {
	DecisionID string `json:"decisionId"`
	Mode       string `json:"mode"`
	Source     string `json:"source"`
}

// Event is one message streamed back to the side panel.
// Type is delta | tool_call | done | error | history | route | status. A
// status (route requests only) has Text starting or routing. A tool_call's
// Text is its snippet (a WebFetch's url), when it has one; a route's is the
// question with any @mode prefix removed, and its other fields are the
// POST /v1/route answer (Source unsupported: the serve has no Auto).
type Event struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	Error string `json:"error,omitempty"`
	Pages []Page `json:"pages,omitempty"`
	Page  *Page  `json:"page"`

	Mode          string             `json:"mode,omitempty"`
	Profile       string             `json:"profile,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Decided       bool               `json:"decided,omitempty"`
	Source        string             `json:"source,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	DecisionID    string             `json:"decisionId,omitempty"`
}

// Turn is one question and answer on a page, as the panel shows it.
type Turn struct {
	At        time.Time `json:"at"`
	Lane      string    `json:"lane"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	ToolCalls []string  `json:"toolCalls"`
}

// Page is a page's conversation in the JSON shape the panel reads (the
// panel is unchanged by the relay).
type Page struct {
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	FirstAsked    time.Time `json:"firstAsked"`
	LastAsked     time.Time `json:"lastAsked"`
	PageSessionID string    `json:"pageSessionId"`
	WorkSessionID string    `json:"workSessionId"`
	Turns         []Turn    `json:"turns"`
	// Truncated is set on a page sent with its older turns left out.
	Truncated bool `json:"truncated,omitempty"`
}
