package relays

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/bropines/rdpanel/internal/store"
)

// CountryPrefixInfo describes a supported country for geographic relay prefix routing.
type CountryPrefixInfo struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	PrefixCount int    `json:"prefix_count"`
}

// CountryNames maps ISO 3166-1 alpha-2 country codes to English names.
var CountryNames = map[string]string{
	"RU": "Russia",
	"DE": "Germany",
	"NL": "Netherlands",
	"US": "United States",
	"KZ": "Kazakhstan",
	"BY": "Belarus",
	"CN": "China",
	"GB": "United Kingdom",
	"FR": "France",
	"TR": "Turkey",
	"UA": "Ukraine",
	"UZ": "Uzbekistan",
	"AM": "Armenia",
	"GE": "Georgia",
	"IN": "India",
	"BR": "Brazil",
	"JP": "Japan",
	"SG": "Singapore",
}

// CountryCIDRs maps country codes to curated aggregated IPv4 prefixes.
var CountryCIDRs = map[string][]string{
	"RU": {
		"95.165.0.0/16", "178.62.0.0/16", "188.162.0.0/15", "217.118.64.0/18",
		"83.149.0.0/16", "194.87.0.0/16", "178.219.160.0/19", "31.134.0.0/15",
		"85.249.0.0/16", "94.25.0.0/16", "109.195.0.0/16", "185.12.0.0/16",
		"77.88.0.0/18", "87.250.250.0/24", "141.101.0.0/16", "213.87.0.0/16",
		"176.59.0.0/16", "212.45.0.0/16", "46.17.40.0/21", "185.165.160.0/22",
	},
	"DE": {
		"85.10.192.0/18", "88.198.0.0/16", "144.76.0.0/16", "148.251.0.0/16",
		"138.201.0.0/16", "195.201.0.0/16", "116.202.0.0/16", "116.203.0.0/16",
		"168.119.0.0/16", "159.69.0.0/16", "78.46.0.0/15", "84.116.0.0/14",
		"217.0.0.0/13", "80.128.0.0/11", "91.0.0.0/10",
	},
	"NL": {
		"185.107.0.0/16", "185.220.0.0/16", "194.187.0.0/16", "89.147.0.0/16",
		"145.220.0.0/16", "94.142.0.0/16", "185.107.56.0/22", "185.14.28.0/22",
		"37.48.64.0/18", "195.169.0.0/16", "77.160.0.0/11", "84.104.0.0/13",
	},
	"US": {
		"104.16.0.0/12", "142.250.0.0/15", "172.217.0.0/16", "23.0.0.0/12",
		"34.0.0.0/10", "35.184.0.0/13", "52.0.0.0/11", "54.144.0.0/12",
		"64.233.160.0/19", "66.102.0.0/20", "13.64.0.0/11", "40.64.0.0/10",
	},
	"KZ": {
		"95.56.0.0/14", "178.88.0.0/13", "2.72.0.0/13", "88.204.128.0/17",
		"185.100.0.0/16", "37.150.0.0/16", "92.46.0.0/15", "89.218.0.0/16",
	},
	"BY": {
		"178.120.0.0/13", "86.57.0.0/16", "93.84.0.0/14", "134.17.0.0/16",
		"194.158.192.0/19", "37.45.0.0/16", "37.212.0.0/14", "82.209.192.0/18",
	},
	"CN": {
		"1.2.0.0/16", "14.144.0.0/12", "27.16.0.0/12", "36.96.0.0/11",
		"42.120.0.0/14", "58.16.0.0/11", "110.16.0.0/12", "117.128.0.0/10",
		"183.0.0.0/10", "223.166.0.0/15",
	},
	"GB": {
		"81.128.0.0/11", "82.0.0.0/11", "86.0.0.0/12", "90.192.0.0/11",
		"109.144.0.0/12", "148.252.0.0/16", "151.224.0.0/12", "213.205.192.0/18",
	},
	"FR": {
		"82.64.0.0/11", "88.160.0.0/11", "90.0.0.0/10", "176.128.0.0/10",
		"51.15.0.0/16", "51.68.0.0/14", "164.132.0.0/16", "212.27.32.0/19",
	},
	"TR": {
		"88.224.0.0/11", "78.160.0.0/11", "176.240.0.0/13", "212.156.0.0/16",
		"195.175.0.0/16", "85.96.0.0/11", "94.54.0.0/15",
	},
	"UA": {
		"178.92.0.0/14", "91.192.0.0/11", "46.118.0.0/15", "94.178.0.0/15",
		"109.86.0.0/15", "194.44.0.0/16", "95.132.0.0/14", "176.36.0.0/14",
	},
	"UZ": {
		"213.230.64.0/18", "84.54.64.0/18", "89.236.192.0/18", "195.158.0.0/19",
		"91.212.88.0/22", "81.95.224.0/20", "217.29.112.0/20",
	},
	"AM": {
		"46.70.0.0/15", "178.134.0.0/16", "212.34.224.0/19", "185.115.0.0/16",
		"37.252.64.0/18", "87.241.128.0/17",
	},
	"GE": {
		"178.134.0.0/15", "31.146.0.0/15", "92.47.0.0/16", "188.129.0.0/16",
		"94.43.0.0/16", "85.117.32.0/19",
	},
	"IN": {
		"49.32.0.0/11", "103.0.0.0/10", "117.192.0.0/10", "157.32.0.0/11",
		"27.56.0.0/13", "182.64.0.0/10", "106.192.0.0/10",
	},
	"BR": {
		"177.0.0.0/9", "179.96.0.0/11", "186.192.0.0/10", "189.0.0.0/9",
		"200.128.0.0/9", "201.0.0.0/9",
	},
	"JP": {
		"118.0.0.0/11", "121.0.0.0/10", "124.32.0.0/11", "126.0.0.0/9",
		"133.0.0.0/10", "153.128.0.0/9",
	},
	"SG": {
		"103.28.0.0/14", "118.189.0.0/16", "175.156.0.0/14", "202.166.0.0/16",
		"128.199.0.0/16", "139.59.192.0/18",
	},
}

// ListCountryPrefixInfo returns supported countries with prefix counts sorted alphabetically.
func ListCountryPrefixInfo() []CountryPrefixInfo {
	list := make([]CountryPrefixInfo, 0, len(CountryNames))
	for code, name := range CountryNames {
		prefixes := CountryCIDRs[code]
		list = append(list, CountryPrefixInfo{
			Code:        code,
			Name:        name,
			PrefixCount: len(prefixes),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// ImportCountryRulesResult holds the result of importing country CIDRs.
type ImportCountryRulesResult struct {
	CountryCode string `json:"country_code"`
	RelayHost   string `json:"relay_host"`
	AddedCount  int    `json:"added_count"`
	TotalRules  int    `json:"total_rules"`
}

// MaxCountryImport bounds the rules one country import may add (the rules
// table and relay_map.txt are not meant for full GeoIP databases).
const MaxCountryImport = 5000

// ImportCountryRules imports the CIDRs of a country pointing to relayHost:
// those of the loaded GeoIP dataset (Server -> Relays -> Update GeoIP), else
// the curated built-in list. Input problems wrap store.ErrInvalid.
func (s *Service) ImportCountryRules(ctx context.Context, countryCode, relayHost, customComment string) (*ImportCountryRulesResult, error) {
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	name, known := CountryNames[code]
	var prefixes []string
	if len(code) == 2 {
		prefixes = s.cfg.GeoIP.CountryCIDRsFor(code)
		if len(prefixes) == 0 {
			prefixes = CountryCIDRs[code]
		}
	}
	if !known && len(prefixes) == 0 {
		return nil, fmt.Errorf("%w: unsupported country code %q", store.ErrInvalid, countryCode)
	}
	if name == "" {
		name = code
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("%w: no CIDRs available for country %s", store.ErrInvalid, code)
	}

	comment := strings.TrimSpace(customComment)
	if comment == "" {
		comment = fmt.Sprintf("[%s] %s", code, name)
	}
	probe := PrefixRule{CIDR: prefixes[0], RelayHost: relayHost, Comment: comment}
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	host := probe.RelayHost

	s.prefixMu.Lock()
	defer s.prefixMu.Unlock()
	existing, err := s.st.ListPrefixRules(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(existing)+len(prefixes))
	for _, r := range existing {
		if r.Validate() == nil {
			seen[r.CIDR] = true
		}
	}
	var add []*PrefixRule
	for _, p := range prefixes {
		rule := &PrefixRule{CIDR: p, RelayHost: host, Comment: comment}
		if rule.Validate() != nil || seen[rule.CIDR] {
			continue
		}
		seen[rule.CIDR] = true
		add = append(add, rule)
	}
	if len(add) > MaxCountryImport {
		return nil, fmt.Errorf("%w: %s has %d prefixes in the loaded GeoIP data; at most %d can be imported at once",
			store.ErrInvalid, code, len(add), MaxCountryImport)
	}
	if len(add) > 0 {
		if err := s.st.CreatePrefixRules(ctx, add); err != nil {
			return nil, err
		}
		if syncErr := s.syncPrefixRulesLocked(ctx); syncErr != nil {
			s.log.Warn("relays: sync prefix rules after country import", slog.Any("err", syncErr))
		}
	}

	return &ImportCountryRulesResult{
		CountryCode: code,
		RelayHost:   host,
		AddedCount:  len(add),
		TotalRules:  len(existing) + len(add),
	}, nil
}
