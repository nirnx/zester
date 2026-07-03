package beacon

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestParseConfig covers the settings-shape parsing: absent/nil keys, both
// services forms, every numeric interval width the YAML and msgpack decoders
// produce, duration strings, defaults, and the reject-whole-section-on-error
// contract.
func TestParseConfig(t *testing.T) {
	svcMap := func(names ...string) map[string]any {
		m := make(map[string]any, len(names))
		for _, n := range names {
			m[n] = map[string]any{}
		}
		return m
	}

	tests := []struct {
		name        string
		settings    map[string]any
		wantService *ServiceConfig
		wantErr     string // substring; "" = no error
	}{
		{
			name:     "nil settings",
			settings: nil,
		},
		{
			name:     "absent beacons key",
			settings: map[string]any{"other": 1},
		},
		{
			name:     "nil beacons key",
			settings: map[string]any{SettingsKey: nil},
		},
		{
			name:     "beacons not a map",
			settings: map[string]any{SettingsKey: []any{"service"}},
			wantErr:  "must be a map",
		},
		{
			name: "service map form with defaults",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx", "redis")},
			}},
			wantService: &ServiceConfig{
				Services:     []string{"nginx", "redis"},
				Interval:     DefaultInterval,
				OnChangeOnly: true,
			},
		},
		{
			name: "service list form sorted",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": []any{"redis", "nginx"}},
			}},
			wantService: &ServiceConfig{
				Services:     []string{"nginx", "redis"},
				Interval:     DefaultInterval,
				OnChangeOnly: true,
			},
		},
		{
			name: "string slice services",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": []string{"b", "a"}},
			}},
			wantService: &ServiceConfig{
				Services:     []string{"a", "b"},
				Interval:     DefaultInterval,
				OnChangeOnly: true,
			},
		},
		{
			name: "service with no body",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": nil,
			}},
			wantService: &ServiceConfig{Interval: DefaultInterval, OnChangeOnly: true},
		},
		{
			name: "int interval seconds",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": 30},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 30 * time.Second, OnChangeOnly: true,
			},
		},
		{
			name: "int64 interval (msgpack snapshot round-trip)",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": int64(5)},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 5 * time.Second, OnChangeOnly: true,
			},
		},
		{
			name: "uint64 interval",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": uint64(7)},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 7 * time.Second, OnChangeOnly: true,
			},
		},
		{
			name: "int8 interval",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": int8(9)},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 9 * time.Second, OnChangeOnly: true,
			},
		},
		{
			name: "float interval",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": 2.5},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 2500 * time.Millisecond, OnChangeOnly: true,
			},
		},
		{
			name: "duration string interval",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": "1m30s"},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: 90 * time.Second, OnChangeOnly: true,
			},
		},
		{
			name: "onchangeonly false",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "onchangeonly": false},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: DefaultInterval, OnChangeOnly: false,
			},
		},
		{
			name: "bad interval rejects whole section",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": "soon"},
			}},
			wantErr: "parse interval",
		},
		{
			name: "zero interval rejected",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": 0},
			}},
			wantErr: "must be positive",
		},
		{
			name: "negative interval rejected",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "interval": -5},
			}},
			wantErr: "must be positive",
		},
		{
			name: "onchangeonly wrong type rejects whole section",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": svcMap("nginx"), "onchangeonly": "yes"},
			}},
			wantErr: "onchangeonly must be a bool",
		},
		{
			name: "bad services entry rejects whole section",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": []any{"nginx", 42}},
			}},
			wantErr: "must be strings",
		},
		{
			name: "empty service name rejected",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": map[string]any{"services": []any{""}},
			}},
			wantErr: "empty service name",
		},
		{
			name: "service section not a map",
			settings: map[string]any{SettingsKey: map[string]any{
				"service": "nginx",
			}},
			wantErr: "must be a map",
		},
		{
			name: "unknown beacon errors, valid service still parsed",
			settings: map[string]any{SettingsKey: map[string]any{
				"diskusage": map[string]any{},
				"service":   map[string]any{"services": svcMap("nginx")},
			}},
			wantService: &ServiceConfig{
				Services: []string{"nginx"}, Interval: DefaultInterval, OnChangeOnly: true,
			},
			wantErr: `unknown beacon "diskusage"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseConfig(tt.settings)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("error = nil, want substring %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
				}
			}
			if !reflect.DeepEqual(cfg.Service, tt.wantService) {
				t.Errorf("Service = %+v, want %+v", cfg.Service, tt.wantService)
			}
		})
	}
}
