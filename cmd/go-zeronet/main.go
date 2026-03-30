package main

import (
	"log"

	"github.com/chenjia404/go-zeronet/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
