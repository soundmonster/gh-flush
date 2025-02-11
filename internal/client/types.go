package client

import "sync"

type Client struct {
	opts          *Options
	notifications []Notification
	input         chan Notification
	statuses      chan NotificationResult
	results       chan NotificationResult
	wgFetcher     *sync.WaitGroup
	wgDeleter     *sync.WaitGroup
}

type Notification struct {
	Id         string
	Reason     string
	Url        string
	Unread     bool
	UpdatedAt  string `json:"updated_at"`
	Repository struct {
		FullName string `json:"full_name"`
	}
	Subject struct {
		Title string
		Url   string
		Type  string
	}
}

type NotificationResult struct {
	Notification Notification
	PR           *PullRequest
	Deleted      bool
	Read         bool
	BotPR        bool
	ClosedPR     bool
}

type PullRequest struct {
	State string
	User  struct {
		Login string
		Type  string
	}
}

type Options struct {
	SkipPRsFromBots       bool
	SkipClosedPRs         bool
	SkipReadNotifications bool
	DryRun                bool
	NumWorkers            int
	HaltAfter             int
}
