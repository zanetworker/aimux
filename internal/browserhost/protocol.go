package browserhost

import "time"

// Request is one message from the Wingman side panel.
// Type is ask | history.list | history.get | history.delete | cancel.
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
}

// Event is one message streamed back to the side panel.
// Type is delta | tool_call | done | error | history.
type Event struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	Error string `json:"error,omitempty"`
	Pages []Page `json:"pages,omitempty"`
	Page  *Page  `json:"page"`
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
