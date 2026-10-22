package domain

type CompletionState struct {
	Continuations int      `json:"continuations"`
	Fingerprint   string   `json:"fingerprint,omitempty"`
	FailureHash   string   `json:"failure_hash,omitempty"`
	Verdict       string   `json:"verdict,omitempty"`
	Missing       []string `json:"missing,omitempty"`
}
