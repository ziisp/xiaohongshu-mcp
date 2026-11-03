//go:build integration

package xiaohongshu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
)

// 手动开启的真实草稿控件验收；只上传独立素材与设置声明，不调用发布函数。
func TestPlatformContentDeclarationsLiveDraft(t *testing.T) {
	if os.Getenv("XHS_PLATFORM_DECLARATIONS_LIVE") != "1" {
		t.Skip("需手动开启真实草稿检查")
	}
	dir := os.Getenv("XHS_DECLARATION_EVIDENCE_DIR")
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0700))
	for _, media := range []string{"image", "video"} {
		t.Run(media, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			b := browser.NewBrowser(true, browser.WithFingerprintSeed(configs.FingerprintSeed()), browser.WithProxy(configs.Proxy()))
			defer b.Close()
			page := b.NewPage().Context(ctx)
			defer page.Close()
			if media == "image" {
				_, err := NewPublishImageAction(page)
				require.NoError(t, err)
				require.NoError(t, uploadImages(page, []string{"/var/lib/xiaohongshu-mcp/ai-acceptance-20261102/ai-declaration-test.png"}))
			} else {
				_, err := NewPublishVideoAction(page)
				require.NoError(t, err)
				require.NoError(t, uploadVideo(page, "/var/lib/xiaohongshu-mcp/ai-acceptance-20261102/ai-declaration-test.mp4"))
			}
			for i, choice := range ContentDeclarationChoices() {
				t.Logf("LIVE_STAGE media=%s choice=%s step=select", media, choice)
				var details *ContentDeclarationDetails
				if choice == "自主拍摄" {
					details = &ContentDeclarationDetails{ShootLocation: "\u5317\u4eac\u7ad9", ShootDate: "2026-11-02"}
				} else if choice == "来源转载" {
					details = &ContentDeclarationDetails{SourceMedia: "控件回归测试媒体"}
				}
				selectionErr := setContentDeclaration(page, choice, details)
				if selectionErr != nil {
					items, _ := page.Elements(".d-modal-content .d-datepicker-content")
					for _, item := range items {
						markup, _ := item.HTML()
						t.Logf("DATE_RENDER_DIAG %s", markup)
					}
				}
				require.NoError(t, selectionErr, "media=%s choice=%s", media, choice)
				if details != nil {
					t.Logf("SOURCE_DETAILS_CONFIRMED choice=%s fields_checked=true modal_closed=true published=false", choice)
				}
				control, value, err := findContentDeclaration(page)
				require.NoError(t, err)
				want := choice
				if choice == NoContentDeclaration {
					want = ""
				}
				require.Equal(t, want, value)
				parent, err := control.Parent()
				require.NoError(t, err)
				shot, err := parent.Screenshot(proto.PageCaptureScreenshotFormatPng, 100)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.png", media, i)), shot, 0600))
				t.Logf("PLATFORM_DECLARATION media=%s choice=%s confirmed=true published=false", media, choice)
			}
			// 原创控件只读检查，不在真实账号上确认原创弹窗。
			cards, err := page.Elements("div.custom-switch-card")
			require.NoError(t, err)
			found := false
			for _, card := range cards {
				text, err := card.Text()
				require.NoError(t, err)
				if strings.Contains(text, "原创声明") {
					sw, err := card.Element("div.d-switch")
					require.NoError(t, err)
					checked, err := originalSwitchChecked(sw)
					require.NoError(t, err)
					t.Logf("ORIGINAL_CONTROL media=%s checked=%t readonly=true", media, checked)
					found = true
					break
				}
			}
			require.True(t, found, "平台应提供原创声明控件")
		})
	}
}
