package relays

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// GeoIPStatus reports the status of the relay geographic lookup engine.
type GeoIPStatus struct {
	Loaded       bool      `json:"loaded"`
	Source       string    `json:"source"`
	CountryCount int       `json:"country_count"`
	PrefixCount  int       `json:"prefix_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GeoIPLookupResult represents the result of looking up an IP address.
type GeoIPLookupResult struct {
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"`
	CountryName string `json:"country_name"`
	MatchedCIDR string `json:"matched_cidr,omitempty"`
	Found       bool   `json:"found"`
}

type prefixEntry struct {
	net         *net.IPNet
	countryCode string
}

// GeoIPEngine provides fast, thread-safe IP-to-country resolution.
type GeoIPEngine struct {
	mu        sync.RWMutex
	prefixes  []prefixEntry
	byCountry map[string][]string
	updatedAt time.Time
	source    string
	cachePath string
}

var (
	defaultEngine *GeoIPEngine
	engineOnce    sync.Once
)

// GetGeoIPEngine returns the singleton GeoIP engine initialized with built-in prefixes.
func GetGeoIPEngine() *GeoIPEngine {
	engineOnce.Do(func() {
		defaultEngine = NewGeoIPEngine("")
		defaultEngine.LoadBuiltin()
	})
	return defaultEngine
}

// NewGeoIPEngine creates a new GeoIP engine.
func NewGeoIPEngine(cachePath string) *GeoIPEngine {
	return &GeoIPEngine{
		byCountry: make(map[string][]string),
		cachePath: cachePath,
		source:    "builtin",
		updatedAt: time.Now().UTC(),
	}
}

// LoadBuiltin loads curated country CIDRs.
func (e *GeoIPEngine) LoadBuiltin() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.prefixes = nil
	e.byCountry = make(map[string][]string)

	for code, cidrs := range CountryCIDRs {
		e.byCountry[code] = append([]string(nil), cidrs...)
		for _, cidr := range cidrs {
			_, ipnet, err := net.ParseCIDR(cidr)
			if err == nil {
				e.prefixes = append(e.prefixes, prefixEntry{net: ipnet, countryCode: code})
			}
		}
	}
	e.source = "builtin"
	e.updatedAt = time.Now().UTC()
}

// Status returns current engine status.
func (e *GeoIPEngine) Status() GeoIPStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return GeoIPStatus{
		Loaded:       len(e.prefixes) > 0,
		Source:       e.source,
		CountryCount: len(e.byCountry),
		PrefixCount:  len(e.prefixes),
		UpdatedAt:    e.updatedAt,
	}
}

// Lookup finds the geographic country for the given IP address.
func (e *GeoIPEngine) Lookup(ipStr string) GeoIPLookupResult {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return GeoIPLookupResult{IP: ipStr, Found: false}
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Check longest prefix match
	var bestMatch *prefixEntry
	var bestMaskLen int

	for i := range e.prefixes {
		if e.prefixes[i].net.Contains(ip) {
			ones, _ := e.prefixes[i].net.Mask.Size()
			if bestMatch == nil || ones > bestMaskLen {
				bestMatch = &e.prefixes[i]
				bestMaskLen = ones
			}
		}
	}

	if bestMatch != nil {
		name := CountryNames[bestMatch.countryCode]
		if name == "" {
			name = bestMatch.countryCode
		}
		return GeoIPLookupResult{
			IP:          ipStr,
			CountryCode: bestMatch.countryCode,
			CountryName: name,
			MatchedCIDR: bestMatch.net.String(),
			Found:       true,
		}
	}

	return GeoIPLookupResult{IP: ipStr, Found: false}
}

// CountryCIDRsFor returns the CIDR prefixes associated with a country code.
func (e *GeoIPEngine) CountryCIDRsFor(code string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return append([]string(nil), e.byCountry[strings.ToUpper(code)]...)
}

// SupportedCountries returns list of countries supported with their prefix count.
func (e *GeoIPEngine) SupportedCountries() []CountryPrefixInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()

	res := make([]CountryPrefixInfo, 0, len(CountryNames))
	for code, name := range CountryNames {
		cnt := len(e.byCountry[code])
		res = append(res, CountryPrefixInfo{
			Code:        code,
			Name:        name,
			PrefixCount: cnt,
		})
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	return res
}

// RefreshFromURL downloads updated country IP ranges and parses CSV/text lines into the engine.
func (e *GeoIPEngine) RefreshFromURL(ctx context.Context, sourceURL string) (GeoIPStatus, error) {
	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = "https://raw.githubusercontent.com/sapics/ip-location-db/main/dbip-country/dbip-country-ipv4.csv"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return e.Status(), err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return e.Status(), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return e.Status(), fmt.Errorf("HTTP error %d fetching geoip database", resp.StatusCode)
	}

	return e.ParseAndLoad(resp.Body, sourceURL)
}

// ParseAndLoad reads CSV format `<start_ip>,<end_ip>,<country_code>` or `<cidr>,<country_code>` and loads into memory.
func (e *GeoIPEngine) ParseAndLoad(r io.Reader, sourceName string) (GeoIPStatus, error) {
	scanner := bufio.NewScanner(r)
	newPrefixes := make([]prefixEntry, 0, 1000)
	newByCountry := make(map[string][]string)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 2 {
			first := strings.Trim(parts[0], `" `)
			second := strings.Trim(parts[1], `" `)

			// Format 1: cidr,country_code
			if strings.Contains(first, "/") {
				code := strings.ToUpper(second)
				_, ipnet, err := net.ParseCIDR(first)
				if err == nil && len(code) == 2 {
					newPrefixes = append(newPrefixes, prefixEntry{net: ipnet, countryCode: code})
					newByCountry[code] = append(newByCountry[code], first)
				}
				continue
			}

			// Format 2: start_ip,end_ip,country_code
			if len(parts) >= 3 {
				code := strings.ToUpper(strings.Trim(parts[2], `" `))
				if len(code) == 2 {
					startIP := net.ParseIP(first).To4()
					endIP := net.ParseIP(second).To4()
					if startIP != nil && endIP != nil {
						cidrs := ipRangeToCIDRs(startIP, endIP)
						for _, cidr := range cidrs {
							_, ipnet, err := net.ParseCIDR(cidr)
							if err == nil {
								newPrefixes = append(newPrefixes, prefixEntry{net: ipnet, countryCode: code})
								newByCountry[code] = append(newByCountry[code], cidr)
							}
						}
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return e.Status(), err
	}

	if len(newPrefixes) == 0 {
		return e.Status(), errors.New("no valid CIDR prefixes parsed from data source")
	}

	e.mu.Lock()
	e.prefixes = newPrefixes
	e.byCountry = newByCountry
	e.source = sourceName
	e.updatedAt = time.Now().UTC()
	e.mu.Unlock()

	// If cache path configured, write cache asynchronously
	if e.cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(e.cachePath), 0755)
		// non-blocking cache write
		go func(path string, prefixes []prefixEntry) {
			f, err := os.Create(path)
			if err != nil {
				return
			}
			defer f.Close()
			w := bufio.NewWriter(f)
			for _, p := range prefixes {
				fmt.Fprintf(w, "%s,%s\n", p.net.String(), p.countryCode)
			}
			_ = w.Flush()
		}(e.cachePath, newPrefixes)
	}

	return e.Status(), nil
}

// ipRangeToCIDRs converts an IPv4 start and end range into a slice of standard CIDRs.
func ipRangeToCIDRs(start, end net.IP) []string {
	var res []string
	startInt := ipToUint32(start)
	endInt := ipToUint32(end)

	for endInt >= startInt {
		maxSize := 32
		for maxSize > 0 {
			mask := uint32(0xFFFFFFFF) << (32 - (maxSize - 1))
			maskBase := startInt & mask
			if maskBase != startInt {
				break
			}
			maxSize--
		}
		x := uint32(1) << (32 - maxSize)
		for (startInt + x - 1) > endInt {
			maxSize++
			x = uint32(1) << (32 - maxSize)
		}
		ip := uint32ToIP(startInt)
		res = append(res, fmt.Sprintf("%s/%d", ip.String(), maxSize))
		startInt += x
		if startInt == 0 {
			break // overflow
		}
	}
	return res
}

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func uint32ToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}
