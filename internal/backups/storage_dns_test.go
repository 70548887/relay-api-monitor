package backups

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"relaylab/internal/config"
	"relaylab/internal/upstream"
)

func TestBackupDNSConfigurationIsValidatedAtManagerConstruction(t *testing.T) {
	m, _ := newTestManager(t)
	opts := m.opts
	opts.BackupDir = filepath.Join(t.TempDir(), "unused-invalid-resolver")
	opts.DNSOverHTTPS = "https://user:private@resolver.example/dns-query"
	if _, err := New(opts); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe resolver accepted or credential exposed: %v", err)
	}
	if _, err := os.Stat(opts.BackupDir); !os.IsNotExist(err) {
		t.Fatal("invalid resolver configuration created backup directories")
	}
	opts.DNSOverHTTPS = "https://1.1.1.1/dns-query"
	configured, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	if configured.storageResolver == nil || configured.opts.DNSOverHTTPS != opts.DNSOverHTTPS {
		t.Fatal("manager did not retain configured shared resolver")
	}
}

func TestBackupDNSConfigFlowsFromYAMLAndEnvironment(t *testing.T) {
	t.Setenv("RELAYLAB_UPSTREAM_DNS_OVER_HTTPS", "")
	t.Setenv("BACKUP_TEST_SAFE_DOH", "https://1.1.1.1/dns-query")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("upstream:\n  dns_over_https: ${BACKUP_TEST_SAFE_DOH}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestManager(t)
	for _, override := range []string{"", "https://8.8.8.8/resolve"} {
		t.Setenv("RELAYLAB_UPSTREAM_DNS_OVER_HTTPS", override)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		opts := m.opts
		opts.DNSOverHTTPS = cfg.Upstream.DNSOverHTTPS
		opts.BackupDir = filepath.Join(t.TempDir(), "config-backups")
		configured, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		configured.Close()
		want := "https://1.1.1.1/dns-query"
		if override != "" {
			want = override
		}
		if configured.storageResolver == nil || configured.opts.DNSOverHTTPS != want {
			t.Fatal("YAML interpolation or environment DNS setting did not reach manager")
		}
	}
}

func TestStoredS3ClientUsesManagerResolverAndPinsConnection(t *testing.T) {
	var calls atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "HEAD" || !strings.HasPrefix(r.Host, "s3.fixture.invalid:") || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("unexpected fixture request")
		}
		w.WriteHeader(200)
	}))
	defer fixture.Close()
	u, _ := url.Parse(fixture.URL)
	endpoint := "http://s3.fixture.invalid:" + u.Port()
	m, _ := newTestManager(t)
	m.opts.AllowedStorageHosts = []string{"s3.fixture.invalid"}
	var resolves atomic.Int32
	m.storageResolver = func(ctx context.Context, host string) ([]netip.Addr, error) {
		resolves.Add(1)
		if host != "s3.fixture.invalid" {
			t.Error("resolved unrelated host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	stored, err := m.SaveStorage(context.Background(), StorageInput{Endpoint: endpoint, Bucket: "fixture-bucket", Region: "auto", ForcePathStyle: true, AccessKeyID: "FIXTURE_ID", SecretAccessKey: "FIXTURE_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	value, client, err := m.storedClient(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: &value.Bucket}); err != nil {
		t.Fatal(err)
	}
	if resolves.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("resolved=%d requests=%d", resolves.Load(), calls.Load())
	}
}

func TestStorageDNSFailuresAreTypedAndNeverDialUnapprovedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []string
		err  error
		code string
	}{
		{"fake", []string{"198.18.0.1"}, nil, "fake_ip_blocked"},
		{"mixed fake public", []string{"8.8.8.8", "198.18.0.1"}, nil, "fake_ip_blocked"},
		{"private", []string{"127.0.0.1"}, nil, "blocked_target"},
		{"mixed public private", []string{"8.8.8.8", "10.0.0.1"}, nil, "blocked_target"},
		{"no answers", nil, nil, "dns_error"},
		{"failed", nil, errors.New("private-config-must-not-leak"), "dns_error"},
		{"DoH policy", nil, &upstream.Error{Code: "blocked_target", Message: "public resolver returned private address"}, "blocked_target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := storageHTTPWithResolver(nil, func(context.Context, string) ([]netip.Addr, error) {
				var out []netip.Addr
				for _, ip := range tc.ips {
					out = append(out, netip.MustParseAddr(ip))
				}
				return out, tc.err
			})
			tr := client.Transport.(boundedStorageTransport).base.(*http.Transport)
			conn, err := tr.DialContext(context.Background(), "tcp", "storage.fixture.invalid:443")
			if conn != nil {
				conn.Close()
				t.Fatal("unapproved address dialed")
			}
			var typed *upstream.Error
			if !errors.As(err, &typed) || typed.Code != tc.code || strings.Contains(err.Error(), "private-config-must-not-leak") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestStorageProtectedHostBlockedBeforeDNS(t *testing.T) {
	var calls atomic.Int32
	client := storageHTTPWithResolver(nil, func(context.Context, string) ([]netip.Addr, error) {
		calls.Add(1)
		return nil, errors.New("DNS must not run")
	})
	tr := client.Transport.(boundedStorageTransport).base.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", net.JoinHostPort("api.zxcbug.com", "443"))
	var typed *upstream.Error
	if calls.Load() != 0 || !errors.As(err, &typed) || typed.Code != "protected_target" {
		t.Fatalf("protected target reached DNS: %v", err)
	}
}

func TestStoragePrivateAllowlistDoesNotIncludeSubdomains(t *testing.T) {
	client := storageHTTPWithResolver([]string{"fixture.invalid"}, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	tr := client.Transport.(boundedStorageTransport).base.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", "other.fixture.invalid:443")
	var typed *upstream.Error
	if !errors.As(err, &typed) || typed.Code != "blocked_target" {
		t.Fatalf("private allowlist included an unconfigured host: %v", err)
	}
}
