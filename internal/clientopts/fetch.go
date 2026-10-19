package clientopts

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultPublicSTUNSource is the community-maintained repository of verified STUN servers.
const DefaultPublicSTUNSource = "https://raw.githubusercontent.com/pradt2/always-online-stun/master/valid_hosts.txt"

// CuratedFallbackSTUNs provides high-availability worldwide STUN servers when remote fetch is unavailable.
var CuratedFallbackSTUNs = []string{
	"stun:stun.cloudflare.com:3478",
	"stun:stun.l.google.com:19302",
	"stun:stun1.l.google.com:19302",
	"stun:stun2.l.google.com:19302",
	"stun:stun.qq.com:3478",
	"stun:stun.syncthing.net:3478",
	"stun:stun.miwifi.com:3478",
	"stun:stun.nextcloud.com:443",
	"stun:stun.sipgate.net:3478",
	"stun:stun.framasoft.org:3478",
	"stun:stun.freeswitch.org:3478",
	"stun:stun.voip.blackberry.com:3478",
}

// PublicSTUNResult contains discovered, probed, and ranked STUN servers.
type PublicSTUNResult struct {
	Source       string        `json:"source"`
	FetchedCount int           `json:"fetched_count"`
	WorkingCount int           `json:"working_count"`
	Items        []ProbeResult `json:"items"`
}

// FetchPublicSTUN downloads a list of public STUN servers, tests reachability concurrently,
// and returns the fastest working servers sorted by latency.
func FetchPublicSTUN(ctx context.Context, sourceURL string, limit int, allowPrivate bool) (*PublicSTUNResult, error) {
	if limit <= 0 {
		limit = 6
	}
	if limit > MaxICEServers {
		limit = MaxICEServers
	}

	rawSource := strings.TrimSpace(sourceURL)
	if rawSource == "" {
		rawSource = DefaultPublicSTUNSource
	}

	u, err := url.Parse(rawSource)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid STUN source URL %q: must be http or https", rawSource)
	}

	candidates, err := downloadSTUNList(ctx, rawSource)
	if err != nil || len(candidates) == 0 {
		// Fallback to curated high-availability STUNs
		candidates = append([]string(nil), CuratedFallbackSTUNs...)
	}

	totalFetched := len(candidates)

	// Pick a diverse sample of up to 20 candidates to probe concurrently
	sample := selectCandidates(candidates, 20)

	results := make([]ProbeResult, len(sample))
	var wg sync.WaitGroup
	for i, s := range sample {
		wg.Add(1)
		go func(idx int, target string) {
			defer wg.Done()
			results[idx] = ProbeOne(ctx, target, allowPrivate)
		}(i, s)
	}
	wg.Wait()

	var working []ProbeResult
	for _, r := range results {
		if r.Reachable {
			working = append(working, r)
		}
	}

	// Sort working servers by RTT ascending (fastest first)
	sort.Slice(working, func(i, j int) bool {
		return working[i].RTTMs < working[j].RTTMs
	})

	workingCount := len(working)
	if len(working) > limit {
		working = working[:limit]
	}

	return &PublicSTUNResult{
		Source:       rawSource,
		FetchedCount: totalFetched,
		WorkingCount: workingCount,
		Items:        working,
	}, nil
}

func downloadSTUNList(ctx context.Context, sourceURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RDPanel/1.0 (+https://github.com/bropines/rdpanel)")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	// Limit response to 512 KiB
	reader := bufio.NewReader(io.LimitReader(resp.Body, 512*1024))
	seen := make(map[string]bool)
	var list []string

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			norm := normalizeSTUN(line)
			if norm != "" && !seen[norm] {
				seen[norm] = true
				list = append(list, norm)
			}
		}
		if err != nil {
			break
		}
	}
	return list, nil
}

func normalizeSTUN(line string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "stun:") && !strings.HasPrefix(s, "stuns:") {
		s = "stun:" + s
	}
	// Check if port exists
	afterScheme := strings.TrimPrefix(s, "stun:")
	afterScheme = strings.TrimPrefix(afterScheme, "stuns:")
	afterScheme = strings.TrimPrefix(afterScheme, "//")
	if !strings.Contains(afterScheme, ":") {
		s = s + ":3478"
	}
	return s
}

func selectCandidates(all []string, maxCount int) []string {
	if len(all) <= maxCount {
		return all
	}
	// Ensure top well-known hosts (like cloudflare/google) are tested first if present
	preferred := []string{"cloudflare", "google", "syncthing", "qq"}
	var top []string
	var rest []string

	for _, item := range all {
		isPref := false
		for _, p := range preferred {
			if strings.Contains(strings.ToLower(item), p) {
				isPref = true
				break
			}
		}
		if isPref {
			top = append(top, item)
		} else {
			rest = append(rest, item)
		}
	}

	// Shuffle the rest deterministically or pseudo-randomly
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(rest), func(i, j int) {
		rest[i], rest[j] = rest[j], rest[i]
	})

	combined := append(top, rest...)
	if len(combined) > maxCount {
		combined = combined[:maxCount]
	}
	return combined
}
