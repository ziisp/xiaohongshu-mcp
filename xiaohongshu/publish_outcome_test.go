package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

func TestPublishRedirectDoesNotProveSubmission(t *testing.T) {
	b := aiTestBrowser(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("login or error")) }))
	defer server.Close()
	for _, path := range []string{"/login?next=/publish/success", "/error", "/publish/success", "/publish/publish"} {
		t.Run(path, func(t *testing.T) {
			p, err := b.Page(proto.TargetCreateTarget{URL: server.URL + path})
			require.NoError(t, err)
			defer p.Close()
			require.NoError(t, p.WaitLoad())
			require.ErrorIs(t, waitPublishSuccess(p, 30*time.Millisecond), ErrPublishResultUnknown, "本地页面或登录错误跳转不证明提交成功")
		})
	}
}

func TestPublishSuccessURLTrustBoundary(t *testing.T) {
	for _, raw := range []string{"https://creator.xiaohongshu.com/publish/success", "https://creator.xiaohongshu.com/publish/success?source=official#done"} {
		require.True(t, isPublishSuccessURL(raw), raw)
	}
	for _, raw := range []string{"", "about:blank", "http://creator.xiaohongshu.com/publish/success", "https://creator.xiaohongshu.com.evil.example/publish/success", "https://evil.example/?next=https://creator.xiaohongshu.com/publish/success", "https://creator.xiaohongshu.com/login?next=/publish/success", "https://creator.xiaohongshu.com/error", "https://creator.xiaohongshu.com/publish/success/", "https://user@creator.xiaohongshu.com/publish/success", "https://creator.xiaohongshu.com:8443/publish/success", "https://creator.xiaohongshu.com/publish/%73uccess"} {
		require.False(t, isPublishSuccessURL(raw), raw)
	}
	require.True(t, errors.Is(publishUnknown(context.DeadlineExceeded), ErrPublishResultUnknown))
}

func TestNativePublishFailurePhases(t *testing.T) {
	b := aiTestBrowser(t)
	for _, video := range []bool{false, true} {
		for _, mode := range []string{"button_missing", "button_disabled", "login_redirect"} {
			t.Run(fmt.Sprintf("video=%t/%s", video, mode), func(t *testing.T) {
				p, submits, _ := aiTestPage(t, b, mode)
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				var err error
				if video {
					err = submitPublishVideo(ctx, p.Context(ctx), "test", "test", nil, nil, "", nil, nil)
				} else {
					err = submitPublish(ctx, p.Context(ctx), "test", "test", nil, nil, false, "", nil, nil)
				}
				require.Error(t, err)
				if mode == "login_redirect" {
					require.ErrorIs(t, err, ErrPublishResultUnknown)
					require.EqualValues(t, 1, submits.Load())
				} else {
					require.NotErrorIs(t, err, ErrPublishResultUnknown)
					require.Zero(t, submits.Load())
				}
			})
		}
	}
}

func TestPublishConfirmationCancellation(t *testing.T) {
	b := aiTestBrowser(t)
	p, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	require.NoError(t, err)
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.Error(t, waitPublishSuccess(p.Context(ctx), 2*time.Second))
	require.Less(t, time.Since(start), time.Second, "取消后应立即报告结果未知")
}
