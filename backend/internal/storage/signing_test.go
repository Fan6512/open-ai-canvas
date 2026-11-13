package storage

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/outbound"
)

func TestNormalizeAccessURLTTL(t *testing.T) {
	tests := []struct {
		name                     string
		access, provider         time.Duration
		wantAccess, wantProvider time.Duration
	}{
		{"defaults", 0, 0, 10 * time.Minute, 10 * time.Minute},
		{"explicit-access", 4 * time.Hour, 0, 4 * time.Hour, 10 * time.Minute},
		{"explicit-provider", 0, 4 * time.Hour, 10 * time.Minute, 4 * time.Hour},
		{"both-explicit", 4 * time.Hour, 4 * time.Hour, 4 * time.Hour, 4 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeSettings(Settings{Runtime: RuntimePolicy{
				AccessURLTTL: tc.access, ProviderAccessURLTTL: tc.provider,
			}})
			if got.Runtime.AccessURLTTL != tc.wantAccess || got.Runtime.ProviderAccessURLTTL != tc.wantProvider {
				t.Fatalf("normalized runtime = %+v, want access %v and provider %v", got.Runtime, tc.wantAccess, tc.wantProvider)
			}
		})
	}
}

func TestDirectSigningPathsAccessURLTTL(t *testing.T) {
	// Capture the signed request at the transport boundary without contacting storage.
	transport := outbound.OutboundHTTPClient(time.Second).Transport.(*http.Transport)
	previous := transport.Proxy
	t.Cleanup(func() { transport.Proxy = previous })
	stopped := errors.New("signed request captured")
	for _, path := range []string{"cdn", "qiniu-s3"} {
		for _, ttl := range []time.Duration{0, 4 * time.Hour} {
			t.Run(path+"/"+ttl.String(), func(t *testing.T) {
				var captured *url.URL
				transport.Proxy = func(req *http.Request) (*url.URL, error) {
					captured = req.URL
					return nil, stopped
				}
				setting := Settings{Provider: qiniuKodoProvider, Region: "z0", Bucket: "bucket", CDNBaseURL: "https://media.example.com", AccessKeyID: "test-id", AccessKeySecret: "test-secret",
					Delivery: DeliverySettings{CDNAuthMode: "qiniu"}, Runtime: RuntimePolicy{AccessURLTTL: ttl}}
				before := time.Now()
				var err error
				if path == "cdn" {
					_, err = getCDNObjectRange(setting, "test.png", "")
				} else {
					_, err = GetQiniuObjectRange(setting, "test.png", "")
				}
				after := time.Now()
				if !errors.Is(err, stopped) || captured == nil {
					t.Fatalf("capture failed: %v", err)
				}
				want := ttl
				if want == 0 {
					want = 600 * time.Second
				}
				var issued, expires time.Time
				if path == "cdn" {
					seconds, err := strconv.ParseInt(captured.Query().Get("e"), 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					expires = time.Unix(seconds, 0)
					issued = before
				} else {
					issued, err = time.Parse("20060102T150405Z", captured.Query().Get("X-Amz-Date"))
					if err != nil {
						t.Fatal(err)
					}
					seconds, err := strconv.ParseInt(captured.Query().Get("X-Amz-Expires"), 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					expires = issued.Add(time.Duration(seconds) * time.Second)
					if issued.Before(before.Truncate(time.Second)) || issued.After(after) {
						t.Fatalf("issued at %v outside request window", issued)
					}
				}
				actualTTL := expires.Sub(issued)
				if actualTTL < want-time.Second || actualTTL > want || expires.After(after.Add(want)) {
					t.Fatalf("signed window %v -> %v, want %v", issued, expires, want)
				}
			})
		}
	}
}

// 展示地址的签名时间由调用方对齐；同一时间窗内重复签发必须得到逐字节相同的 URL，
// 否则浏览器 HTTP 缓存（以完整 URL 为键）在每次刷新画布时都会失效并重新下载对象。
func TestDisplayURLIsDeterministicForAlignedSigningTime(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	signedAt := time.Now().UTC().Truncate(time.Hour)
	expiresAt := signedAt.Add(24 * time.Hour)
	cases := map[string]Settings{
		"s3":      {Provider: s3Provider, Region: "cn-nb1", Endpoint: server.URL, Bucket: "bucket", AccessKeyID: "access-id", AccessKeySecret: "secret-value"},
		"qiniu":   {Provider: qiniuKodoProvider, Endpoint: "https://up-z0.qiniup.com", Bucket: "bucket", AccessKeyID: "access-id", AccessKeySecret: "secret-value"},
		"tencent": {Provider: tencentCOSProvider, Endpoint: "https://cos.ap-guangzhou.myqcloud.com", Bucket: "bucket-1250000000", AccessKeyID: "access-id", AccessKeySecret: "secret-value"},
		"aliyun":  {Provider: aliyunOSSProvider, Endpoint: "https://oss-cn-test.aliyuncs.com", Bucket: "bucket", AccessKeyID: "access-id", AccessKeySecret: "secret-value"},
	}
	for name, setting := range cases {
		t.Run(name, func(t *testing.T) {
			first, err := SignedOriginObjectDisplayURL(setting, "users/u-1/image/a b.png", signedAt, expiresAt)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond) // 跨过秒级时间戳，证明结果不依赖 time.Now。
			second, err := SignedOriginObjectDisplayURL(setting, "users/u-1/image/a b.png", signedAt, expiresAt)
			if err != nil {
				t.Fatal(err)
			}
			if first != second {
				t.Fatalf("display URL is not stable:\n%s\n%s", first, second)
			}
			query := mustParseQuery(t, first)
			if got := query.Get("response-cache-control"); got != "private, max-age=86400, immutable" {
				t.Fatalf("response-cache-control = %q in %s", got, first)
			}
			if strings.Contains(first, "secret-value") {
				t.Fatal("signed URL leaked secret key")
			}
			switch name {
			case "s3", "qiniu":
				if query.Get("X-Amz-Date") != signedAt.Format("20060102T150405Z") || query.Get("X-Amz-Expires") != "86400" {
					t.Fatalf("SigV4 window = %s/%s", query.Get("X-Amz-Date"), query.Get("X-Amz-Expires"))
				}
			case "tencent":
				window := strings.Join([]string{itoa(signedAt.Unix()), itoa(expiresAt.Unix())}, ";")
				if query.Get("q-sign-time") != window || query.Get("q-key-time") != window {
					t.Fatalf("COS window = %s", first)
				}
			}
		})
	}
}

func TestOneShotURLsKeepCurrentTimeSigningWithoutCacheOverride(t *testing.T) {
	setting := Settings{Provider: qiniuKodoProvider, Endpoint: "https://up-z0.qiniup.com", Bucket: "bucket", AccessKeyID: "access-id", AccessKeySecret: "secret-value"}
	value, err := SignedOriginObjectDownloadURL(setting, "users/u-1/video/clip.mp4", time.Now().Add(time.Hour), "镜头.mp4")
	if err != nil {
		t.Fatal(err)
	}
	query := mustParseQuery(t, value)
	if query.Get("response-cache-control") != "" || !strings.HasPrefix(query.Get("response-content-disposition"), "attachment") {
		t.Fatalf("download URL = %s", value)
	}
	signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
	if err != nil || time.Since(signedAt) > time.Minute {
		t.Fatalf("download URL must be signed at the current time, got %q", query.Get("X-Amz-Date"))
	}
}

func TestPublicCDNObjectDownloadURLForcesAttachmentOnlyOnDownloadURL(t *testing.T) {
	setting := Settings{CDNBaseURL: "https://media.example.com"}
	value, err := PublicCDNObjectDownloadURL(setting, "users/u-1/image/test image.png", "下载图片.png")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "media.example.com" || parsed.Path != "/users/u-1/image/test image.png" || !strings.HasPrefix(parsed.Query().Get("response-content-disposition"), "attachment") {
		t.Fatalf("download URL = %q", value)
	}
	inline, err := OssCDNObjectURL(setting.CDNBaseURL, "users/u-1/image/test image.png")
	if err != nil {
		t.Fatal(err)
	}
	if parsedInline, err := url.Parse(inline); err != nil || parsedInline.RawQuery != "" {
		t.Fatalf("inline CDN URL = %q, %v", inline, err)
	}
}

func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
