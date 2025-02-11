package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sync"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/cli/go-gh/v2/pkg/api"
)

const (
	BotPR    = "🤖"
	ClosedPR = "✅"
	Read     = "👓"
	Deleted  = "❌"
)

func NewClient() *Client {
	client := new(Client)
	client.opts = parseOptions()
	client.input = make(chan Notification, client.opts.NumWorkers)
	client.statuses = make(chan NotificationResult, client.opts.NumWorkers)
	client.results = make(chan NotificationResult)
	client.wgFetcher = new(sync.WaitGroup)
	client.wgDeleter = new(sync.WaitGroup)
	return client
}

func parseOptions() *Options {
	opts := new(Options)
	flag.BoolVarP(&opts.SkipPRsFromBots, "skip-bots", "b", false, "don't delete notifications on PRs from bots")
	flag.BoolVarP(&opts.SkipClosedPRs, "skip-closed", "c", false, "don't delete notifications on closed / merged PRs")
	flag.BoolVarP(&opts.SkipReadNotifications, "skip-read", "r", false, "don't delete read notifications")
	flag.BoolVarP(&opts.DryRun, "dry-run", "n", false, "dry run without deleting anything")
	flag.IntVarP(&opts.NumWorkers, "workers", "w", runtime.NumCPU(), "number of workers")
	// TODO get rid of this and store offsets in a file
	flag.IntVarP(&opts.HaltAfter, "halt-after", "s", 50, "stop after a given number of read messages in a row, set to 0 to never stop")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "`gh flush` deletes all GitHub notifications that are from bots,\nand/or are about closed pull requests\n\nUsage:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) != 0 {
		flag.Usage()
		msg := fmt.Sprintf("unexpected arguments: %v", args)
		panic(msg)
	}
	return opts
}

func (client *Client) FetchNotifications() {
	requestPath := "notifications?all=true"
	page := 1
	ghApiClient, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}

	readStreak := 0
	notifications := []Notification{}

loadNotifications:
	for {
		response, err := ghApiClient.Request(http.MethodGet, requestPath, nil)
		notificationBatch := []Notification{}
		decoder := json.NewDecoder(response.Body)
		err = decoder.Decode(&notificationBatch)
		if err != nil {
			panic(err)
		}
		if err := response.Body.Close(); err != nil {
			fmt.Println(err)
		}
		for _, notification := range notificationBatch {
			if notification.Unread {
				readStreak = 0
			} else {
				readStreak++
				if client.opts.HaltAfter > 0 && readStreak >= client.opts.HaltAfter {
					break loadNotifications
				}
			}
			notifications = append(notifications, notification)
		}

		var hasNextPage bool
		if requestPath, hasNextPage = findNextPage(response); !hasNextPage {
			break loadNotifications
		}
		page++
	}
	client.notifications = notifications
}

var linkRE = regexp.MustCompile(`<([^>]+)>;\s*rel="([^"]+)"`)

func findNextPage(response *http.Response) (string, bool) {
	for _, m := range linkRE.FindAllStringSubmatch(response.Header.Get("Link"), -1) {
		if len(m) > 2 && m[2] == "next" {
			return m[1], true
		}
	}
	return "", false
}

func (client *Client) NotificationCount() int {
	return len(client.notifications)
}

func (client *Client) ProcessNotifications() {
	client.wgFetcher.Add(client.opts.NumWorkers)
	client.wgDeleter.Add(client.opts.NumWorkers)

	go func() {
		defer close(client.input)
		for _, n := range client.notifications {
			client.input <- n
		}
	}()

	for i := 0; i < client.opts.NumWorkers; i++ {
		go client.tagNotifications()
		go client.deleteNotifications()
	}

	go func() { defer close(client.statuses); client.wgFetcher.Wait() }()
	go func() { defer close(client.results); client.wgDeleter.Wait() }()
}

func (client *Client) GetNotificationResult() (NotificationResult, bool) {
	result, ok := <-client.results
	return result, ok
}

func (client *Client) tagNotifications() {
	defer client.wgFetcher.Done()

	ghApiClient, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}
	for notification := range client.input {
		result := NotificationResult{Notification: notification}

		if !notification.Unread && !client.opts.SkipReadNotifications {
			result.Read = true
		}

		if notification.Subject.Type == "PullRequest" {

			pr := new(PullRequest)
			err := ghApiClient.Get(notification.Subject.Url, &pr)
			if err != nil {
				panic(err)
			}
			result.PR = pr
			result.BotPR = from_a_bot(pr)
			result.ClosedPR = closedPR(pr)
		}
		client.statuses <- result
	}
}

func read(notification Notification) bool {
	return !notification.Unread
}
func from_a_bot(pullRequest *PullRequest) bool {
	return pullRequest.User.Type == "Bot"
}

func closedPR(pullRequest *PullRequest) bool {
	return pullRequest.State == "closed"
}

func (client *Client) deleteNotifications() {
	defer client.wgDeleter.Done()
	ghApiClient, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}

	for status := range client.statuses {
		if status.BotPR && !client.opts.SkipPRsFromBots {
			status.Deleted = true
		}
		if status.ClosedPR && !client.opts.SkipClosedPRs {
			status.Deleted = true
		}
		if status.Read && !client.opts.SkipReadNotifications {
			status.Deleted = true
		}

		if status.Deleted && !client.opts.DryRun {
			err := ghApiClient.Delete(status.Notification.Url, nil)
			if err != nil {
				panic(err)
			}
		}

		client.results <- status
	}
}

func (client *Client) PrintResults() {
	fmt.Println("Time                \tReason [Repo] Title")

	result, ok := client.GetNotificationResult()
	for ok {
		reason := ""
		if result.Deleted {
			reason += Deleted
		}
		if result.Read {
			reason += Read
		}
		if result.ClosedPR {
			reason += ClosedPR
		}
		if result.BotPR {
			reason += BotPR
		}

		if reason != "" {
			reason += " "
		}

		ts := result.Notification.UpdatedAt.Format(time.RFC3339)
		fmt.Printf("%s\t%s[%s] %s\n", ts, reason, result.Notification.Repository.FullName, result.Notification.Subject.Title)
		result, ok = client.GetNotificationResult()
	}
}
