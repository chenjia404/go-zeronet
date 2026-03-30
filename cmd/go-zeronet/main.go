package main

import (
	"log"
	"os"

	"github.com/chenjia404/go-zeronet/internal/app"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "site" {
		if err := app.RunSiteCommand(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
