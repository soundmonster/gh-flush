package main

import (
	"os"

	"github.com/soundmonster/gh-flush/internal/client"
	"github.com/soundmonster/gh-flush/internal/ui"
)

func main() {
	client := client.NewClient()
	if isTerminal() {
		ui.Run(client)
	} else {
		client.FetchNotifications()
		client.ProcessNotifications()
		client.PrintResults()
	}
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		panic(err)
	}
	return (fi.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}
