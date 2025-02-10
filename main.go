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

var skipPRsFromBots bool
var skipClosedPRs bool
var skipReadNotifications bool
var dryRun bool
var numWorkers int
var haltAfter int

func main() {
	flag.BoolVar(&skipPRsFromBots, "skip-bots", false, "don't delete notifications on PRs from bots")
	flag.BoolVar(&skipClosedPRs, "skip-closed", false, "don't delete notifications on closed / merged PRs")
	flag.BoolVar(&skipReadNotifications, "skip-read", false, "don't delete read notifications")
	flag.BoolVar(&dryRun, "dry-run", false, "dry run without deleting anything")
	flag.IntVar(&numWorkers, "workers", runtime.NumCPU(), "number of workers")
	// TODO get rid of this and store offsets in a file
	flag.IntVar(&haltAfter, "halt-after", 50, "stop after a given number of read messages in a row, set to 0 to never stop")
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

	notifications := make(chan Notification, numWorkers)
	statuses := make(chan NotificationResult, numWorkers)
	results := make(chan NotificationResult, numWorkers)
	notifications_arr := fetchNotifications()

	go func() {
		defer close(notifications)
		for _, n := range notifications_arr {
			notifications <- n
		}
	}()

	wg_fetcher := new(sync.WaitGroup)
	wg_fetcher.Add(numWorkers)
	wg_deleter := new(sync.WaitGroup)
	wg_deleter.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go tagNotifications(notifications, statuses, wg_fetcher)
		go deleteNotifications(statuses, results, wg_deleter)
	}

	go func() { wg_fetcher.Wait(); close(statuses) }()
	go func() { wg_deleter.Wait(); close(results) }()

	printResults(results)
	fmt.Println("Done 🎉")
}

func fetchNotifications() []Notification {
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
				if haltAfter > 0 && readStreak >= haltAfter {
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

func tagNotifications(notifications <-chan Notification, statuses chan<- NotificationResult, wg *sync.WaitGroup) {
	defer wg.Done()

	client, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}
	for notification := range notifications {
		result := NotificationResult{Notification: notification}

		if !notification.Unread && !skipReadNotifications {
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

func deleteNotifications(statuses <-chan NotificationResult, results chan<- NotificationResult, wg *sync.WaitGroup) {
	defer wg.Done()
	client, err := api.DefaultRESTClient()
	if err != nil {
		panic(err)
	}

	for status := range statuses {
		if status.BotPR && !skipPRsFromBots {
			status.Deleted = true
		}
		if status.ClosedPR && !skipClosedPRs {
			status.Deleted = true
		}
		if status.Read && !skipReadNotifications {
			status.Deleted = true
		}

		if status.Deleted && !dryRun {
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
