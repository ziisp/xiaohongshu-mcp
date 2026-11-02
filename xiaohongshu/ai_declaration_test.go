package xiaohongshu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

// 使用真实浏览器与本地表单，断言发布请求，而不是模拟点击函数。
func aiTestBrowser(t *testing.T) *rod.Browser {
	t.Helper()
	bin := os.Getenv("XHS_TEST_BROWSER")
	if bin == "" {
		t.Skip("设置 XHS_TEST_BROWSER 运行原生浏览器回归")
	}
	l := launcher.New().Bin(bin).Headless(true).NoSandbox(true).UserDataDir(t.TempDir())
	u, err := l.Launch()
	require.NoError(t, err)
	b := rod.New().ControlURL(u)
	require.NoError(t, b.Connect())
	// 仅在隔离测试浏览器中提供可信成功页夹具，不访问真实平台。
	router := b.HijackRequests()
	router.MustAdd("https://creator.xiaohongshu.com/publish/success*", func(h *rod.Hijack) {
		h.Response.SetHeader("Content-Type", "text/html; charset=utf-8").SetBody("<html>发布成功</html>")
	})
	go router.Run()
	t.Cleanup(func() { _ = router.Stop() })
	t.Cleanup(func() { _ = b.Close(); l.Cleanup() })
	return b
}

func aiTestPage(t *testing.T, b *rod.Browser, mode string) (*rod.Page, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	var submits atomic.Int32
	var declared atomic.Bool
	selectHTML := `<div class="d-select" id="declaration"><div class="d-select-content" onclick="document.getElementById('d-portal').hidden=false"><div class="d-select-description"></div><div class="d-select-placeholder">添加内容类型声明</div></div></div>`
	if mode == "already_selected" {
		selectHTML = `<div class="d-select" id="declaration"><div class="d-select-content"><div class="d-select-description">笔记含AI合成内容</div><div class="d-select-placeholder"></div></div></div>`
	}
	if mode == "missing_select" {
		selectHTML = ""
	}
	if mode == "menu_text_only" {
		selectHTML = `<div class="d-select" id="declaration"><div class="d-select-content"><div class="d-select-placeholder">添加内容类型声明</div></div><div class="d-option-name">笔记含AI合成内容</div></div>`
	}
	optionText := "笔记含AI合成内容"
	if mode == "missing_option" {
		optionText = "个人观点，仅供参考"
	}
	selected := `document.querySelector('#declaration .d-select-description').textContent='笔记含AI合成内容';document.querySelector('#declaration .d-select-placeholder').textContent='';document.getElementById('ai').value='1';this.parentElement.hidden=true`
	if mode == "not_retained" {
		selected = `this.parentElement.hidden=true`
	}
	if mode == "menu_text_only" {
		selected = `this.parentElement.hidden=true`
	}
	optionAttrs := ""
	if mode == "disabled" {
		optionAttrs = ` aria-disabled="true"`
	}
	buttonHTML := `<button class="bg-red">发布</button>`
	if mode == "button_missing" {
		buttonHTML = ""
	}
	if mode == "button_disabled" {
		buttonHTML = `<button class="bg-red" disabled>发布</button>`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			require.NoError(t, r.ParseForm())
			submits.Add(1)
			declared.Store(r.Form.Get("ai") == "1")
			target := "https://creator.xiaohongshu.com/publish/success?source=official"
			if mode == "login_redirect" {
				target = "/login"
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/login" {
			_, _ = fmt.Fprint(w, "login")
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><style>body{padding:30px}.d-select-placeholder,.d-option-name,button{padding:16px;cursor:pointer}#d-portal{position:fixed;top:160px;background:#eee;z-index:2}[contenteditable]{height:80px}</style>
<form action="/submitted" method="post"><div class="d-input"><input></div><div contenteditable="true" role="textbox"></div>
%s<input id="ai" name="ai" type="hidden" value="0"><div class="publish-page-publish-btn">%s</div></form>
<div id="d-portal" hidden><div class="d-option-name"%s onclick="%s"><span>%s</span></div></div>`, selectHTML, buttonHTML, optionAttrs, selected, optionText)
	}))
	t.Cleanup(server.Close)
	p, err := b.Page(proto.TargetCreateTarget{URL: server.URL + "/publish/publish"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	require.NoError(t, p.WaitLoad())
	return p, &submits, &declared
}

func TestAIContentDeclarationSelection(t *testing.T) {
	b := aiTestBrowser(t)
	for _, tc := range []struct {
		mode string
		ok   bool
	}{
		{"normal", true}, {"already_selected", true}, {"missing_select", false},
		{"missing_option", false}, {"not_retained", false}, {"disabled", false}, {"menu_text_only", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p, submits, _ := aiTestPage(t, b, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err := setAIContentDeclaration(p.Context(ctx))
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, submits.Load(), "设置声明本身不能发布")
			if tc.ok {
				text, err := p.MustElement("#declaration").Text()
				require.NoError(t, err)
				require.Contains(t, text, "笔记含AI合成内容")
			}
		})
	}
}

// 两条真实提交路径默认先确认声明；仅显式 false 跳过。
func TestPublishStopsWhenAIContentDeclarationFails(t *testing.T) {
	b := aiTestBrowser(t)
	aiOn, aiOff := true, false
	for _, video := range []bool{false, true} {
		for _, tc := range []struct {
			name, mode                  string
			ai                          *bool
			wantPublished, wantDeclared bool
		}{
			{"default_success", "normal", nil, true, true},
			{"default_unconfirmed", "not_retained", nil, false, false},
			{"ai_success", "normal", &aiOn, true, true},
			{"ai_unconfirmed", "not_retained", &aiOn, false, false},
			{"explicit_opt_out", "missing_select", &aiOff, true, false},
		} {
			t.Run(fmt.Sprintf("video=%t/%s", video, tc.name), func(t *testing.T) {
				p, submits, declared := aiTestPage(t, b, tc.mode)
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				var err error
				if video {
					err = submitPublishVideo(ctx, p.Context(ctx), "test", "test", nil, nil, "", nil, tc.ai)
				} else {
					err = submitPublish(ctx, p.Context(ctx), "test", "test", nil, nil, false, "", nil, tc.ai)
				}
				if tc.wantPublished {
					require.NoError(t, err)
					require.EqualValues(t, 1, submits.Load())
					require.Equal(t, tc.wantDeclared, declared.Load())
				} else {
					require.Error(t, err)
					require.True(t, strings.Contains(err.Error(), "AI"), "错误应说明 AI 声明失败: %v", err)
					require.Zero(t, submits.Load(), "声明失败不得提交")
				}
			})
		}
	}
}
