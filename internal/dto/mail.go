package dto

// Mail is one transactional email. At least one of Text/HTML must be set;
// with both, the message is multipart/alternative (D-103).
type Mail struct {
	To      []string `json:"to"`
	ReplyTo string   `json:"replyTo,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
}
