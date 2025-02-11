package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sync"

	flag "github.com/spf13/pflag"

	"github.com/cli/go-gh/v2/pkg/api"
)

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
	Deleted      bool
	Read         bool
	BotPR        bool
	ClosedPR     bool
}

type PullRequest struct {
	State string
	User  struct{ Type string }
}

const (
	BotPR    = "🤖"
	ClosedPR = "✅"
	Read     = "👓"
	Deleted  = "❌"
)

type Options struct {
	SkipPRsFromBots       bool
	SkipClosedPRs         bool
	SkipReadNotifications bool
	DryRun                bool
	NumWorkers            int
	HaltAfter             int
}

func main() {
	opts := parseOptions()
	notifications := make(chan Notification, opts.NumWorkers)
	statuses := make(chan NotificationResult, opts.NumWorkers)
	results := make(chan NotificationResult, opts.NumWorkers)
	notifications_arr := fetchNotifications(opts)

	go func() {
		defer close(notifications)
		for _, n := range notifications_arr {
			notifications <- n
		}
	}()

	wg_fetcher := new(sync.WaitGroup)
	wg_fetcher.Add(opts.NumWorkers)
	wg_deleter := new(sync.WaitGroup)
	wg_deleter.Add(opts.NumWorkers)

	for i := 0; i < opts.NumWorkers; i++ {
		go tagNotifications(notifications, statuses, wg_fetcher, opts)
		go deleteNotifications(statuses, results, wg_deleter, opts)
	}

	go func() { wg_fetcher.Wait(); close(statuses) }()
	go func() { wg_deleter.Wait(); close(results) }()

	printResults(results)
	fmt.Println("Done 🎉")
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

func fetchNotifications(opts *Options) []Notification {
	requestPath := "notifications?all=true"
	page := 1
	client, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}

	readStreak := 0
	notification_arr := []Notification{}

	fmt.Printf("Fetching notifications... ")

loadNotifications:
	for {
		response, err := client.Request(http.MethodGet, requestPath, nil)
		notifications := []Notification{}
		decoder := json.NewDecoder(response.Body)
		err = decoder.Decode(&notifications)
		if err != nil {
			panic(err)
		}
		if err := response.Body.Close(); err != nil {
			fmt.Println(err)
		}
		for _, notification := range notifications {
			if notification.Unread {
				readStreak = 0
			} else {
				readStreak++
				if opts.HaltAfter > 0 && readStreak >= opts.HaltAfter {
					break loadNotifications
				}
			}
			notification_arr = append(notification_arr, notification)
		}

		var hasNextPage bool
		if requestPath, hasNextPage = findNextPage(response); !hasNextPage {
			break loadNotifications
		}
		page++
	}
	fmt.Printf("done. Loaded %d notifications.\n", len(notification_arr))

	return notification_arr
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

func tagNotifications(notifications <-chan Notification, statuses chan<- NotificationResult, wg *sync.WaitGroup, opts *Options) {
	defer wg.Done()

	client, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}
	for notification := range notifications {
		result := NotificationResult{Notification: notification}

		if !notification.Unread && !opts.SkipReadNotifications {
			result.Read = true
		}

		if notification.Subject.Type == "PullRequest" {

			pr := new(PullRequest)
			err := client.Get(notification.Subject.Url, &pr)
			if err != nil {
				panic(err)
			}
			result.BotPR = from_a_bot(pr)
			result.ClosedPR = closedPR(pr)
		}
		statuses <- result
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

func deleteNotifications(statuses <-chan NotificationResult, results chan<- NotificationResult, wg *sync.WaitGroup, opts *Options) {
	defer wg.Done()
	client, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}

	for status := range statuses {
		if status.BotPR && !opts.SkipPRsFromBots {
			status.Deleted = true
		}
		if status.ClosedPR && !opts.SkipClosedPRs {
			status.Deleted = true
		}
		if status.Read && !opts.SkipReadNotifications {
			status.Deleted = true
		}

		if status.Deleted && !opts.DryRun {
			err := client.Delete(status.Notification.Url, nil)
			if err != nil {
				panic(err)
			}
		}
		results <- status
	}
}

func printResults(results <-chan NotificationResult) {
	fmt.Println("Time                \tReason [Repo] Title")

	for result := range results {
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

		fmt.Printf("%s\t%s[%s] %s\n", result.Notification.UpdatedAt, reason, result.Notification.Repository.FullName, result.Notification.Subject.Title)
	}
}
