package reports

import (
	"strings"
	"testing"
)

func TestCurrentSignalsSQLHasOnlySourceListPlaceholders(t *testing.T) {
	if got := strings.Count(currentSignalsSQL, "?"); got != 2 {
		t.Fatalf("currentSignalsSQL has %d question marks; want only the 2 source-list bind placeholders", got)
	}
}
