package analytics

import (
	"strings"
	"testing"
)

const validBatch = `{"v":1,"id":"00112233aabbccdd","p":"/uk/","l":"uk","r":"www.google.com",
 "u":{"s":"google","m":"cpc","c":"mikrotik","t":"","n":""},"ad":"g",
 "e":[{"t":"pageview","o":0},{"t":"section","x":"hero","o":120},{"t":"click","x":"cta-telegram","o":5400},
      {"t":"scroll","v":50,"o":6000},{"t":"time","v":6100,"o":6100},{"t":"section_time","x":"hero","v":4000,"o":6100},
      {"t":"outbound","x":"t.me","o":5410},{"t":"egg","x":"konami","o":5900}]}`

func TestParseBatchAcceptsAValidBatch(t *testing.T) {
	batch, err := ParseBatch([]byte(validBatch))
	if err != nil {
		t.Fatal(err)
	}
	if batch.Path != "/uk/" || batch.Lang != "uk" || batch.Referrer != "www.google.com" || len(batch.Events) != 8 {
		t.Errorf("parsed: %+v", batch)
	}
	if batch.UTM.Campaign != "mikrotik" || batch.AdClick != "g" {
		t.Errorf("utm: %+v ad: %q", batch.UTM, batch.AdClick)
	}
}

// jsonBell is the JSON escape of the BEL control character, spelled so that no tool on the way
// to this file can turn it into the character itself.
var jsonBell = string(rune(92)) + "u0007"

func TestParseBatchRejects(t *testing.T) {
	event := func(body string) string {
		return `{"v":1,"id":"00112233aabbccdd","p":"/","e":[` + body + `]}`
	}
	many := strings.TrimSuffix(strings.Repeat(`{"t":"pageview","o":0},`, maxEvents+1), ",")

	cases := map[string]string{
		"not JSON":                "pageview",
		"unknown field":           `{"v":1,"id":"00112233aabbccdd","p":"/","e":[{"t":"pageview","o":0}],"cookie":"x"}`,
		"trailing data":           `{"v":1,"id":"00112233aabbccdd","p":"/","e":[{"t":"pageview","o":0}]} {"v":1}`,
		"wrong version":           `{"v":2,"id":"00112233aabbccdd","p":"/","e":[{"t":"pageview","o":0}]}`,
		"short id":                `{"v":1,"id":"0011","p":"/","e":[{"t":"pageview","o":0}]}`,
		"id that is not hex":      `{"v":1,"id":"zz112233aabbccdd","p":"/","e":[{"t":"pageview","o":0}]}`,
		"path with a query":       `{"v":1,"id":"00112233aabbccdd","p":"/?email=a@b.c","e":[{"t":"pageview","o":0}]}`,
		"path that is a URL":      `{"v":1,"id":"00112233aabbccdd","p":"https://evil.example/","e":[{"t":"pageview","o":0}]}`,
		"path with markup":        `{"v":1,"id":"00112233aabbccdd","p":"/<script>","e":[{"t":"pageview","o":0}]}`,
		"language that is a word": `{"v":1,"id":"00112233aabbccdd","p":"/","l":"english","e":[{"t":"pageview","o":0}]}`,
		"referrer with a path":    `{"v":1,"id":"00112233aabbccdd","p":"/","r":"google.com/search?q=secret","e":[{"t":"pageview","o":0}]}`,
		"unknown ad marker":       `{"v":1,"id":"00112233aabbccdd","p":"/","ad":"gclid-value-123","e":[{"t":"pageview","o":0}]}`,
		"utm with a control char": strings.ReplaceAll(`{"v":1,"id":"00112233aabbccdd","p":"/","u":{"s":"aBELLb"},"e":[{"t":"pageview","o":0}]}`, "BELL", jsonBell),
		"utm that is too long":    `{"v":1,"id":"00112233aabbccdd","p":"/","u":{"c":"` + strings.Repeat("x", 101) + `"},"e":[{"t":"pageview","o":0}]}`,
		"no events":               `{"v":1,"id":"00112233aabbccdd","p":"/","e":[]}`,
		"too many events":         event(many),
		"unknown event type":      event(`{"t":"keypress","x":"a","o":1}`),
		"scroll to 33 percent":    event(`{"t":"scroll","v":33,"o":1}`),
		"click without a target":  event(`{"t":"click","o":1}`),
		"target with markup":      event(`{"t":"click","x":"<img src=x>","o":1}`),
		"target with spaces":      event(`{"t":"click","x":"cta telegram","o":1}`),
		"target on a pageview":    event(`{"t":"pageview","x":"something","o":0}`),
		"negative offset":         event(`{"t":"pageview","o":-5}`),
		"offset beyond a day":     event(`{"t":"pageview","o":90000000}`),
		"negative time":           event(`{"t":"time","v":-1,"o":1}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBatch([]byte(body)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestParseBatchRejectsOversizedBody(t *testing.T) {
	body := `{"v":1,"id":"00112233aabbccdd","p":"/","e":[{"t":"pageview","o":0}],"pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	if _, err := ParseBatch([]byte(body)); err == nil {
		t.Error("accepted")
	}
}
