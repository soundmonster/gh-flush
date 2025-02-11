package main

import (
	"github.com/soundmonster/gh-flush/internal/client"
	"github.com/soundmonster/gh-flush/internal/ui"
)

func main() {
	client := client.NewClient()
	ui.Run(client)
	// TODO handle character device output
	// if this is not a TTY:
	// client.FetchNotifications()
	// client.ProcessNotifications()
	// client.PrintResults()
	// fmt.Println("Done 🎉")
}
