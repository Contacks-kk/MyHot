package grouping

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseBatchResultMatchesSchemaDefaultsAndDecisionCompleteness(t *testing.T) {
	data := []byte(`{"query":"launch","decisions":[{"id":"C2","relation":"SAME_STORY","confidence":0.8},{"id":"C1","relation":"UNRELATED","confidence":0.3,"note":"note","ignored":true}],"selection":{"addsValue":true,"reason":"  新增报道  "},"ignored":true}`)
	result, err := ParseBatchResult(data, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "launch" || result.Decisions[0].ID != "C2" || result.Decisions[0].Note != "" || result.Decisions[1].Note != "note" ||
		!result.Selection.AddsValue || result.Selection.Reason != "新增报道" {
		t.Fatalf("parsed batch response differs from schema behavior: %#v", result)
	}

	query, _ := json.Marshal(strings.Repeat("q", 401))
	note, _ := json.Marshal(strings.Repeat("n", 401))
	catchData := []byte(`{"decisions":[{"id":"C1","relation":"SAME_OCCURRENCE","confidence":1,"note":` + string(note) + `}],"selection":{"addsValue":false,"reason":"ok"},"query":` + string(query) + `}`)
	result, err = ParseBatchResult(catchData, 1)
	if err != nil || result.Query != "" || result.Decisions[0].Note != "" {
		t.Fatalf("catch(\"\") defaults changed: result=%#v err=%v", result, err)
	}
}

func TestParseBatchResultRejectsInvalidOrIncompleteJudgements(t *testing.T) {
	validSelection := `"selection":{"addsValue":true,"reason":"valid"}`
	tests := []struct {
		name string
		data string
	}{
		{name: "missing decisions", data: `{` + validSelection + `}`},
		{name: "missing candidate", data: `{"decisions":[],` + validSelection + `}`},
		{name: "duplicate candidate", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1},{"id":"C1","relation":"UNRELATED","confidence":1}],` + validSelection + `}`},
		{name: "invalid candidate id", data: `{"decisions":[{"id":"c1","relation":"UNRELATED","confidence":1}],` + validSelection + `}`},
		{name: "invalid relation", data: `{"decisions":[{"id":"C1","relation":"TYPO","confidence":1}],` + validSelection + `}`},
		{name: "confidence over one", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1.01}],` + validSelection + `}`},
		{name: "confidence string", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":"1"}],` + validSelection + `}`},
		{name: "blank reason", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1}],"selection":{"addsValue":true,"reason":" \t "}}`},
		{name: "reason too long", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1}],"selection":{"addsValue":true,"reason":` + mustJSON(strings.Repeat("中", 401)) + `}}`},
		{name: "selection missing", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1}]}`},
		{name: "boolean required", data: `{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":1}],"selection":{"addsValue":null,"reason":"valid"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseBatchResult([]byte(test.data), 1); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}

func TestParsePairAndSignalResults(t *testing.T) {
	pair, err := ParsePairResult([]byte(`{"relation":"ROUNDUP","confidence":0.75,"a":false,"b":"ok","difference":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Relation != RelationRoundup || pair.Confidence != .75 || pair.A != "" || pair.B != "ok" || pair.Difference != "" {
		t.Fatalf("pair schema catch defaults changed: %#v", pair)
	}
	if _, err := ParsePairResult([]byte(`{"relation":"SAME_STORY","confidence":1.1}`)); err == nil {
		t.Fatal("invalid pair confidence was accepted")
	}

	signal, err := ParseSignalResult([]byte(`{"decisions":[{"id":"C2","relation":"SAME_STORY","confidence":0.8},{"id":"C1","relation":"UNRELATED","confidence":0}]}`), 2)
	if err != nil || len(signal.Decisions) != 2 {
		t.Fatalf("signal result=%#v err=%v", signal, err)
	}
	if _, err := ParseSignalResult([]byte(`{"decisions":[{"id":"C1","relation":"UNRELATED","confidence":0}]}`), 2); err == nil {
		t.Fatal("incomplete signal answer was accepted")
	}
}

func mustJSON(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}
