package promptpath

import (
	"os"
	"strings"
)

/** Configured returns the configured prompt directory. */
func Configured() string {
	return strings.TrimSpace(os.Getenv("INDUSTRY_PROMPT_DIR"))
}
