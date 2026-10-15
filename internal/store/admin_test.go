package store

import (
	"context"
	"testing"
)

// The SQL OS-family expression must classify exactly like OSFamily.
func TestOSFamilySQLMatchesGo(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	cases := map[string]string{
		"windows / Windows 11 Pro - 10.0.22631": OSWindows,
		"Mac OS 14.1":                           OSMacOS,
		"macOS / 13.5":                          OSMacOS,
		"Darwin 23":                             OSMacOS,
		"android / Android 14":                  OSAndroid,
		"iOS 17":                                OSIOS,
		"linux / Ubuntu 22.04":                  OSLinux,
		"Debian GNU 12":                         OSLinux,
		"":                                      OSOther,
		"Haiku R1":                              OSOther,
	}
	i := 0
	for os, want := range cases {
		if got := OSFamily(os); got != want {
			t.Errorf("OSFamily(%q) = %q, want %q", os, got, want)
		}
		i++
		d := &Device{RustdeskID: "dev" + string(rune('a'+i)), OS: os}
		if err := s.Devices.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		n, err := s.Devices.Count(ctx, DeviceSearch{OSFamily: want, Query: d.RustdeskID})
		if err != nil || n != 1 {
			t.Errorf("SQL family of %q is not %q (n=%d, err=%v)", os, want, n, err)
		}
	}
	_, oses, _, err := s.Devices.Facets(ctx, DeviceSearch{})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range oses {
		total += f.Count
	}
	if total != len(cases) {
		t.Fatalf("facets: %v", oses)
	}
}

func TestPresenceSamplesKeepMax(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	now := clk.now()
	ps := s.PresenceSamples()
	for _, n := range []int{3, 9, 4} {
		if err := ps.Record(ctx, now, n); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ps.Since(ctx, HourStart(now))
	if err != nil || m[HourStart(now).UnixMilli()] != 9 {
		t.Fatalf("samples: %v %v", m, err)
	}
}
