package sites

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSiteValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		port int
		ok   bool
	}{
		{"my-app", 3000, true},
		{"a", 65535, true},
		{"", 3000, false},
		{"my-app.localhost", 3000, false},
		{"my-app-", 3000, false},
		{"../my-app", 3000, false},
		{"my-app", 8443, false},
		{"my-app", 0, false},
		{"my-app", 65536, false},
	} {
		if err := validate(test.name, 8443, test.port); (err == nil) != test.ok {
			t.Errorf("validate(%q, %d): %v", test.name, test.port, err)
		}
	}
}

func TestCaddyConfigIsLocalAndDoesNotInstallTrust(t *testing.T) {
	data, err := caddyConfig("my-app.localhost", 8443, 3000, "/private/site/data")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Admin struct {
			Disabled bool
		}
		Apps struct {
			PKI struct {
				Authorities map[string]struct {
					InstallTrust *bool `json:"install_trust"`
				} `json:"certificate_authorities"`
			}
			HTTP struct {
				Servers map[string]struct {
					Listen    []string
					Protocols []string
					HTTPS     struct {
						DisableRedirects bool `json:"disable_redirects"`
					} `json:"automatic_https"`
					Routes []struct {
						Match  []struct{ Host []string }
						Handle []struct {
							Upstreams []struct{ Dial string }
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	trust := config.Apps.PKI.Authorities["lenv"].InstallTrust
	if !config.Admin.Disabled || trust == nil || *trust {
		t.Fatalf("unexpected admin or trust configuration: %s", data)
	}
	server := config.Apps.HTTP.Servers["site"]
	if len(config.Apps.HTTP.Servers) != 1 || !slices.Equal(server.Listen, []string{"127.0.0.1:8443"}) ||
		!server.HTTPS.DisableRedirects || !slices.Equal(server.Protocols, []string{"h1", "h2"}) {
		t.Fatalf("unexpected listeners: %s", data)
	}
	if len(server.Routes) != 2 || len(server.Routes[0].Match) != 1 ||
		!slices.Equal(server.Routes[0].Match[0].Host, []string{"my-app.localhost"}) ||
		len(server.Routes[0].Handle) != 1 || len(server.Routes[0].Handle[0].Upstreams) != 1 ||
		server.Routes[0].Handle[0].Upstreams[0].Dial != "127.0.0.1:3000" {
		t.Fatalf("unexpected routing: %s", data)
	}
}

func TestMissingCaddyDoesNotCreateState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("sites run as a regular user")
	}
	t.Setenv("PATH", t.TempDir())
	root := filepath.Join(t.TempDir(), "state")
	m := New(root, "")
	if _, err := m.Start(context.Background(), "test", 3000, ""); err == nil || !strings.Contains(err.Error(), "daemon's PATH") {
		t.Fatalf("expected missing Caddy error, got %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("unexpected state after failed launch: %v", err)
	}
	if m.Status().Running || m.Stop().Running {
		t.Fatal("proxy should not be running")
	}
}
