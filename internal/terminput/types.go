// Package terminput types text and keys into Claude Code sessions that are
// open in a terminal, after checking what the screen shows.
package terminput

// Key is a single key sent to a terminal, such as "1" for a prompt option.
type Key string

// Option is one numbered choice in a permission prompt.
type Option struct {
	Key   Key
	Label string
}

// Prompt is a Claude Code permission prompt read from the screen.
type Prompt struct {
	// ID is stable for the same body, question and labels, so a retried key
	// can be matched against the prompt it was meant for.
	ID       string
	Question string
	// Detail is what the prompt is about: the lines between the rule above
	// it and the question, such as "Bash command" and the command, or
	// "Create file", the name and a preview. Dedented, at most 20 lines and
	// 2,000 characters; empty when no rule is found above the question.
	Detail  string
	Options []Option
}
