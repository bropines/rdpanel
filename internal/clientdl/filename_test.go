package clientdl

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testKey = "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=" // 44 characters, like every real key

// plainFileName is the name WindowsFileName would build with no length limit
// and no whitespace shift.
func plainFileName(t *testing.T, s ServerSettings) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return "rustdesk-licensed-" + reverse(base64.RawURLEncoding.EncodeToString(raw)) + ".exe"
}

// The limit is about the whole path, not the name: the longest name must still
// fit a long download directory plus the " (1)" a browser appends.
func TestMaxFileNameLeavesRoomForDirectory(t *testing.T) {
	const (
		maxPath  = 260 // classic Windows MAX_PATH, terminating NUL included
		dirChars = 56  // C:\Users\<30 characters>\Downloads\
	)
	if got := dirChars + maxFileName + len(" (1)"); got > maxPath-1 {
		t.Fatalf("directory (%d) + name (%d) + \" (1)\" = %d, over %d", dirChars, maxFileName, got, maxPath-1)
	}
}

// A settings set that encodes to a name under the budget is produced, stays
// under it and reads back.
func TestWindowsFileNameUnderBudget(t *testing.T) {
	s := ServerSettings{Host: "d.example.org", Key: testKey, API: "https://p.example"}
	name, err := WindowsFileName(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(name) > maxFileName {
		t.Fatalf("name is %d characters, budget %d", len(name), maxFileName)
	}
	if got, ok := ParseWindowsFileName(name); !ok || got != s {
		t.Fatalf("round trip %q = %+v %v", name, got, ok)
	}
	dir := `C:\Users\Firstname.Lastname-Company\Downloads\`
	for _, n := range []string{dir + name, dir + strings.TrimSuffix(name, ".exe") + " (1).exe"} {
		if len(n) >= 260 {
			t.Fatalf("%d characters: still over MAX_PATH", len(n))
		}
		if got, ok := ParseWindowsFileName(n); !ok || got != s {
			t.Fatalf("round trip %q = %+v %v", n, got, ok)
		}
	}
}

// The limit is exact: growing the host one character at a time, the last name
// produced is within the budget (and not far under it), and the first refusal
// is a name that really would be longer than the budget.
func TestWindowsFileNameBudgetEdge(t *testing.T) {
	var last string
	for n := 1; n < 200; n++ {
		s := ServerSettings{Host: strings.Repeat("a", n) + ".ex", Key: testKey, API: "https://p.ex"}
		name, err := WindowsFileName(s)
		if err != nil {
			if !errors.Is(err, ErrFileName) {
				t.Fatalf("err = %v", err)
			}
			if got := len(plainFileName(t, s)); got <= maxFileName {
				t.Fatalf("refused although the name is only %d characters", got)
			}
			if last == "" {
				t.Fatal("nothing fits")
			}
			if len(last) < maxFileName-4 {
				t.Fatalf("last name is %d characters, budget %d: limit applied too early", len(last), maxFileName)
			}
			return
		}
		if len(name) > maxFileName {
			t.Fatalf("host length %d: name is %d characters, budget %d", n, len(name), maxFileName)
		}
		if got, ok := ParseWindowsFileName(name); !ok || got != s {
			t.Fatalf("round trip %q = %+v %v", name, got, ok)
		}
		last = name
	}
	t.Fatal("never refused")
}

// A normal deployment (host, relay, https API, 44-character key) is refused,
// not squeezed into a name Windows may cut. The name below is 240 characters:
// it fits the 255 that NTFS allows for one name, which is what used to let it
// through, but not a path in an ordinary Downloads folder (the broken download
// that prompted this limit arrived as 230 of these 240 characters).
func TestWindowsFileNameRealisticDeploymentRefused(t *testing.T) {
	s := ServerSettings{
		Host:  "desk-ru-api.bropinus.ru",
		Relay: "desk-ru.bropines.ru:21117",
		Key:   testKey,
		API:   "https://desk-ru-api.bropinus.ru",
	}
	if got := len(plainFileName(t, s)); got != 240 {
		t.Fatalf("example encodes to %d characters, the test assumes 240", got)
	}
	name, err := WindowsFileName(s)
	if !errors.Is(err, ErrFileName) {
		t.Fatalf("err = %v, name = %q", err, name)
	}
	if name != "" {
		t.Fatalf("a name came back with the error: %q", name)
	}
}

// Whitespace shifts must still be tried when the first alignment is unusable:
// settings whose plain encoding contains "--" and that fit once shifted get a
// name; the same settings over the budget are refused whatever the shift.
func TestWindowsFileNameShiftKeepsBudget(t *testing.T) {
	for r := rune(0x80); r < 0x3000; r++ {
		s := ServerSettings{Host: "d" + string(r) + ".ex", Key: testKey, API: "https://p.ex"}
		raw, _ := json.Marshal(s)
		if !strings.Contains(reverse(base64.RawURLEncoding.EncodeToString(raw)), "--") {
			continue
		}
		name, err := WindowsFileName(s)
		if err != nil {
			t.Fatalf("%q: %v", s.Host, err)
		}
		if len(name) > maxFileName || strings.Contains(name, "--") {
			t.Fatalf("%q: %d characters", name, len(name))
		}
		if got, ok := ParseWindowsFileName(name); !ok || got != s {
			t.Fatalf("round trip %q = %+v", name, got)
		}
		s.Host = strings.Repeat("a", 120) + string(r)
		if _, err := WindowsFileName(s); !errors.Is(err, ErrFileName) {
			t.Fatalf("over budget: err = %v", err)
		}
		return
	}
	t.Fatal("found no settings whose plain encoding contains \"--\"")
}
