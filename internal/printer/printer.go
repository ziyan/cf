// Package printer writes what a command has to say.
package printer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

var (
	JSONOutput bool
	Stdout     io.Writer = os.Stdout
	Stderr     io.Writer = os.Stderr
)

func PrintJSON(value interface{}) {
	encoder := json.NewEncoder(Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func PrintInfo(message string, arguments ...interface{}) {
	_, _ = fmt.Fprintf(Stdout, message+"\n", arguments...)
}

func PrintError(message string, arguments ...interface{}) {
	_, _ = fmt.Fprintf(Stderr, "Error: "+message+"\n", arguments...)
}

func PrintSuccess(message string, arguments ...interface{}) {
	_, _ = fmt.Fprintf(Stdout, "✓ "+message+"\n", arguments...)
}
