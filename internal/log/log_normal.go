// THIS IS DISABLED PLUSbuild !vicos

package log

import (
	"fmt"
	"os"
	"strings"
)

func Println(a ...interface{}) (int, error) {
	return fmt.Println(a...)
}

// Printf always ends with a newline so journal lines don't glue together
// (MemProbe + Xiaozhi were concatenating on one line).
func Printf(format string, a ...interface{}) (int, error) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	return fmt.Printf(format, a...)
}

func Errorln(a ...interface{}) (int, error) {
	return fmt.Fprintln(os.Stderr, a...)
}

func Errorf(format string, a ...interface{}) (int, error) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	return fmt.Fprintf(os.Stderr, format, a...)
}
