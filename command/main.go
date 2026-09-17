package main

import (
	"os"

	"github.com/ziyan/cf/internal/commands"
	"github.com/ziyan/cf/internal/printer"
)

func main() {
	if err := commands.Execute(); err != nil {
		printer.PrintError("%v", err)
		os.Exit(1)
	}
}
