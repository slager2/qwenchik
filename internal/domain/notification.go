package domain

type NotificationDelivery struct {
	TaskID      string `json:"task_id"`
	TerminalSeq int64  `json:"terminal_seq"`
	ClaimID     string `json:"claim_id"`
	Attempt     int    `json:"attempt"`
	Channel     string `json:"channel"`
	Outcome     string `json:"outcome"`
}
