package xiaohongshu

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

const creatorUploadFixture = `<div class="upload-content">
<div class="creator-tab">上传图文</div><div class="creator-tab">上传视频</div>
<form method="post" action="/submitted"><input type="file"><button>发布</button></form></div>`

func TestCreatorPublishGate(t *testing.T) {
	browser := aiTestBrowser(t)
	for _, tc := range []struct {
		name, document, wantError, tab string
		staleCookie                    bool
		timeout                        time.Duration
	}{
		{name: "no_cookie_ready", document: creatorUploadFixture},
		{name: "video_entry", document: creatorUploadFixture, tab: "上传视频"},
		{name: "delayed_controls", document: `<div id="target"></div><script>setTimeout(() => {
document.getElementById('target').innerHTML = ` + "`" + creatorUploadFixture + "`" + `; }, 450)</script>`},
		{name: "missing_controls", document: `<p>正在加载</p>`, timeout: 1500 * time.Millisecond, wantError: "未就绪"},
		{name: "stale_cookie_login_redirect", document: `<meta http-equiv="refresh" content="0;url=/login">`, staleCookie: true, timeout: 2 * time.Second, wantError: "请重新登录"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = page.Close() })
			if tc.staleCookie {
				require.NoError(t, browser.SetCookies([]*proto.NetworkCookieParam{
					{Name: "galaxy_creator_session_id", Value: "fixture-stale", Domain: "creator.xiaohongshu.com", Path: "/", Secure: true},
				}))
			}
			var writes atomic.Int32
			router := page.HijackRequests()
			router.MustAdd("*", func(h *rod.Hijack) {
				h.Response.SetHeader("Content-Type", "text/html; charset=utf-8")
				if h.Request.Method() != http.MethodGet {
					writes.Add(1)
				}
				switch h.Request.URL().Path {
				case "/publish/publish":
					h.Response.SetBody("<!doctype html>" + tc.document)
				case "/login":
					h.Response.SetBody("<html>扫码登录</html>")
				default:
					h.Response.SetBody("<html>创作中心</html>")
				}
			})
			go router.Run()
			t.Cleanup(func() { _ = router.Stop() })

			timeout := tc.timeout
			if timeout == 0 {
				timeout = 6 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			tab := tc.tab
			if tab == "" {
				tab = "上传图文"
			}
			err = openCreatorPublishPage(page.Context(ctx), tab)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				if tc.wantError == "未就绪" {
					require.NotContains(t, err.Error(), "请重新登录")
				}
			} else {
				require.NoError(t, err)
				info, err := page.Info()
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(info.URL, urlOfPublic), info.URL)
			}
			require.Zero(t, writes.Load(), "只读门禁不应提交表单")
		})
	}
}
