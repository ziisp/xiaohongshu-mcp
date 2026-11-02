//go:build integration

package xiaohongshu

import (
	"context"
	"image"
	"image/color"
	"image/png"
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

// 只上传独立测试素材并选择声明；此测试没有调用任何提交发布函数。
func TestAIContentDeclarationLiveDraft(t *testing.T) {
	if os.Getenv("XHS_AI_LIVE_DRAFT") != "1" {
		t.Skip("手工开启的真实平台草稿检查")
	}
	dir := os.Getenv("XHS_AI_EVIDENCE_DIR")
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0700))
	imagePath := filepath.Join(dir, "ai-declaration-test.png")
	f, err := os.Create(imagePath)
	require.NoError(t, err)
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 170, 255})
		}
	}
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())
	for _, media := range []string{"image", "video"} {
		t.Run(media, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			b := browser.NewBrowser(true, browser.WithFingerprintSeed(configs.FingerprintSeed()), browser.WithProxy(configs.Proxy()))
			defer b.Close()
			p := b.NewPage().Context(ctx)
			defer p.Close()
			if media == "image" {
				_, err = NewPublishImageAction(p)
				require.NoError(t, err)
				require.NoError(t, uploadImages(p, []string{imagePath}))
			} else {
				_, err = NewPublishVideoAction(p)
				require.NoError(t, err)
				require.NoError(t, uploadVideo(p, filepath.Join(dir, "ai-declaration-test.mp4")))
			}
			elems, err := p.Elements(".d-select-content")
			require.NoError(t, err)
			for _, e := range elems {
				text, _ := e.Text()
				if strings.Contains(text, "声明") {
					html, _ := e.HTML()
					t.Logf("DECLARATION_UI %s", html)
				}
			}
			selectionErr := setAIContentDeclaration(p)
			options, _ := p.Elements(".d-option-name")
			for _, option := range options {
				text, _ := option.Text()
				t.Logf("MENU_OPTION %s", text)
			}
			portals, _ := p.Elements("[id*='portal']")
			for _, portal := range portals {
				id, _ := portal.Attribute("id")
				if id != nil {
					t.Logf("PORTAL_ID %s", *id)
				}
			}
			values, _ := p.Elements(".d-select-content")
			for _, value := range values {
				html, _ := value.HTML()
				t.Logf("SELECT_VALUE %s", html)
			}
			require.NoError(t, selectionErr)
			e, selected, err := findAIContentDeclaration(p)
			require.NoError(t, err)
			require.True(t, selected)
			html, err := e.HTML()
			require.NoError(t, err)
			t.Logf("SELECTED_UI %s", html)
			shot, err := e.Screenshot(proto.PageCaptureScreenshotFormatPng, 100)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, media+"-ai-selected.png"), shot, 0600))
			t.Logf("AI_SELECTED media=%s published=false", media)
		})
	}
}
