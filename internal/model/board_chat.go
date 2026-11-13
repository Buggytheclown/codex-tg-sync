package model

type CreateBoardChatInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	AttemptID       string `json:"attempt_id"`
}
type BoardChatResult struct {
	ThreadID string `json:"thread_id"`
	Fresh    bool   `json:"fresh"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	Name     string `json:"name,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	Model    string `json:"model,omitempty"`
}
