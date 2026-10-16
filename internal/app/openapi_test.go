package app

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// adminBase is where the panel API is mounted (api/openapi.yaml servers).
const adminBase = "/api/v1/admin"

// openAPIExceptions lists "METHOD /path" (relative to adminBase) that are
// intentionally present on only one side. Keep it empty unless there is a
// documented reason.
var openAPIExceptions = map[string]string{}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

func normPath(p string) string { return pathParam.ReplaceAllString(p, "{}") }

// TestOpenAPIMatchesRouter walks the real router and checks that every
// /api/v1/admin route is documented in api/openapi.yaml and every documented
// operation is routed.
func TestOpenAPIMatchesRouter(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	type server struct {
		URL string `yaml:"url"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	external := map[string]bool{} // "METHOD /full/path" for paths with their own servers
	for p, item := range spec.Paths {
		if raw, ok := item["servers"]; ok {
			// Outside the admin base path (/api/v1/events, /api/v1/openapi.*).
			b, _ := yaml.Marshal(raw)
			var servers []server
			if err := yaml.Unmarshal(b, &servers); err != nil || len(servers) != 1 {
				t.Fatalf("%s: want exactly one server, got %v", p, raw)
			}
			for m := range item {
				if m == "get" || m == "post" || m == "put" || m == "patch" || m == "delete" {
					external[strings.ToUpper(m)+" "+normPath(servers[0].URL+p)] = true
				}
			}
			continue
		}
		for m := range item {
			switch m {
			case "get", "post", "put", "patch", "delete":
				documented[strings.ToUpper(m)+" "+normPath(p)] = true
			}
		}
	}

	a := newTestApp(t, nil)
	routes, ok := a.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	routed := map[string]bool{}
	err = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(route, "/*/", "/")
		delete(external, method+" "+normPath(strings.TrimSuffix(strings.TrimSuffix(route, "/*"), "/")))
		if !strings.HasPrefix(route, adminBase+"/") {
			return nil
		}
		routed[method+" "+normPath(strings.TrimPrefix(route, adminBase))] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(routed) < 50 {
		t.Fatalf("walked only %d admin routes", len(routed))
	}
	var missingDoc, missingRoute []string
	for k := range routed {
		if !documented[k] && openAPIExceptions[k] == "" {
			missingDoc = append(missingDoc, k)
		}
	}
	for k := range documented {
		if !routed[k] && openAPIExceptions[k] == "" {
			missingRoute = append(missingRoute, k)
		}
	}
	for k := range external {
		missingRoute = append(missingRoute, k)
	}
	sort.Strings(missingDoc)
	sort.Strings(missingRoute)
	if len(missingDoc) > 0 {
		t.Errorf("routes missing from api/openapi.yaml:\n  %s", strings.Join(missingDoc, "\n  "))
	}
	if len(missingRoute) > 0 {
		t.Errorf("documented operations without a route:\n  %s", strings.Join(missingRoute, "\n  "))
	}
}
