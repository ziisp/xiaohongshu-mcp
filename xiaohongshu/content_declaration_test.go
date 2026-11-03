package xiaohongshu

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

func TestResolveContentDeclarationPolicy(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		label string
		ai    *bool
		want  string
		bad   bool
	}{
		{"", nil, aiDeclarationLabel, false}, {"", &on, aiDeclarationLabel, false}, {"", &off, "", false},
		{"自主拍摄", nil, "自主拍摄", false}, {"来源转载", &off, "来源转载", false},
		{NoContentDeclaration, nil, NoContentDeclaration, false}, {"个人观点，仅供参考", nil, "", true},
		{"内容来源声明", nil, "", true}, {"自主拍摄", &on, "", true}, {aiDeclarationLabel, &off, "", true},
	} {
		got, err := ResolveContentDeclaration(tc.label, tc.ai)
		if tc.bad {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}

func TestDeclarationClassTokens(t *testing.T) {
	require.True(t, hasExactClass("d-option selected", "d-option"))
	require.False(t, hasExactClass("d-option-name", "d-option"))
	require.False(t, hasExactClass("not-disabled", "disabled"))
}

// 本地真实表单模拟真实的二级来源菜单和悬停清除按钮，不访问业务平台。
func declarationFixture(t *testing.T, b *rod.Browser, initial, mode string) (*rod.Page, *atomic.Int32, *atomic.Bool, *atomic.Value) {
	t.Helper()
	var submits atomic.Int32
	var original atomic.Bool
	var declared atomic.Value
	declared.Store("")
	placeholder := declarationPlaceholder
	if initial != "" {
		placeholder = ""
	}
	var options, source strings.Builder
	for _, choice := range ContentDeclarationChoices() {
		if choice == NoContentDeclaration {
			continue
		}
		attrs := ""
		if mode == "disabled" {
			attrs = ` aria-disabled="true"`
		}
		row := fmt.Sprintf(`<div class="d-option"%s><div class="d-option-description"><div class="d-option-name"><span onclick="choose('%s')">%s</span></div></div></div>`, attrs, choice, choice)
		if choice == "自主拍摄" || choice == "来源转载" {
			source.WriteString(row)
		} else {
			options.WriteString(row)
		}
	}
	if mode != "missing_submenu" {
		fmt.Fprintf(&options, `<div class="d-option-name" onmouseenter="document.getElementById('source').hidden=false"><span>内容来源声明</span></div><div id="source" hidden>%s</div>`, source.String())
	}
	retain := mode != "not_retained"
	clearRetain := mode != "clear_not_retained"
	originalRetain := mode != "original_not_retained"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			require.NoError(t, r.ParseForm())
			submits.Add(1)
			declared.Store(r.Form.Get("declaration"))
			original.Store(r.Form.Get("original") == "1")
			http.Redirect(w, r, "https://creator.xiaohongshu.com/publish/success?source=official", http.StatusSeeOther)
			return
		}
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><style>body{padding:30px}.d-select-main{position:relative;width:340px}.d-select-content,.d-option-name,.d-switch,.d-checkbox,button{padding:12px;cursor:pointer}#portal{position:fixed;top:180px;left:30px;width:340px;background:#eee;z-index:4}#source{position:absolute;left:340px;top:0;background:#eee;width:240px}.d-select-suffix-clear{display:none;position:absolute;right:0;top:8px;padding:6px}.d-select-main:hover .d-select-suffix-clear{display:block}[contenteditable]{height:70px}.footer{position:fixed;z-index:10;top:70px;left:50px;background:#ccc;padding:25px}.d-switch input,.d-checkbox input{position:absolute;pointer-events:none;opacity:0}</style>
<script>function choose(v){if(%t){document.getElementById('original').checked=false;}if(%t){document.getElementById('value').textContent=v;document.getElementById('placeholder').textContent='';document.getElementById('declared').value=v;}document.getElementById('portal').hidden=true}function clearDeclaration(){if(%t){document.getElementById('value').textContent='';document.getElementById('placeholder').textContent='%s';document.getElementById('declared').value='';}}function acceptOriginal(){if(%t){document.getElementById('original').checked=true;}document.getElementById('footer').hidden=true}</script>
<form method="post" action="/submit"><div class="d-input"><input></div><div contenteditable="true" role="textbox"></div><div class="d-select-main"><div class="d-select-content" onclick="document.getElementById('portal').hidden=false"><div id="value" class="d-select-description">%s</div><div id="placeholder" class="d-select-placeholder">%s</div></div><span class="d-select-suffix-clear" onclick="clearDeclaration()">×</span></div><input id="declared" type="hidden" name="declaration" value="%s"><div class="custom-switch-card">原创声明<div class="d-switch" onclick="document.getElementById('footer').hidden=false"><input id="original" name="original" value="1" type="checkbox">开关</div></div><div class="publish-page-publish-btn"><button class="bg-red">发布</button></div></form>
<div id="portal" hidden>%s</div><div class="footer" id="footer" hidden>原创声明须知<div class="d-checkbox" onclick="document.getElementById('terms').checked=!document.getElementById('terms').checked;document.getElementById('accept').disabled=!document.getElementById('terms').checked"><input id="terms" type="checkbox">同意须知</div><button id="accept" class="custom-button" disabled onclick="acceptOriginal()">声明原创</button></div>`, mode == "original_reset_later", retain, clearRetain, declarationPlaceholder, originalRetain, html.EscapeString(initial), placeholder, html.EscapeString(initial), options.String())
		fmt.Fprint(w, sourceDialogFixture(mode))
	}))
	t.Cleanup(server.Close)
	p, err := b.Page(proto.TargetCreateTarget{URL: server.URL + "/publish/publish"})
	require.NoError(t, err)
	t.Cleanup(func() { p.Close() })
	require.NoError(t, p.WaitLoad())
	return p, &submits, &original, &declared
}

func TestUnconfirmedSourceModalStopsNative(t *testing.T) {
	b := aiTestBrowser(t)
	p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, "source_modal")
	require.Error(t, setContentDeclaration(p, "自主拍摄"), "标签变化不能代替必填弹窗确认")
	require.Zero(t, submits.Load())
}

func TestPlatformDeclarationSelectionNative(t *testing.T) {
	b := aiTestBrowser(t)
	for _, choice := range ContentDeclarationChoices() {
		t.Run(choice, func(t *testing.T) {
			initial := aiDeclarationLabel
			if choice == aiDeclarationLabel {
				initial = "自主拍摄"
			}
			p, submits, _, _ := declarationFixture(t, b, initial, "normal")
			require.NoError(t, setContentDeclaration(p, choice, testDeclarationDetails(choice)))
			_, got, err := findContentDeclaration(p)
			require.NoError(t, err)
			want := choice
			if choice == NoContentDeclaration {
				want = ""
			}
			require.Equal(t, want, got)
			require.Zero(t, submits.Load())
		})
	}
}

func TestPlatformDeclarationFailureStopsNative(t *testing.T) {
	b := aiTestBrowser(t)
	for _, tc := range []struct{ choice, mode string }{{"自主拍摄", "missing_submenu"}, {"内容包含营销广告", "not_retained"}, {"虚构演绎，仅供娱乐", "disabled"}, {NoContentDeclaration, "clear_not_retained"}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			require.Error(t, setContentDeclaration(p.Context(ctx), tc.choice, testDeclarationDetails(tc.choice)))
			require.Zero(t, submits.Load())
		})
	}
}

func TestOriginalMustBeConfirmedNative(t *testing.T) {
	b := aiTestBrowser(t)
	p, submits, _, _ := declarationFixture(t, b, "", "original_not_retained")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.Error(t, setOriginal(p.Context(ctx)), "点击声明按钮但开关未保留不能算成功")
	require.Zero(t, submits.Load())
}

func TestOriginalResetByContentChoiceStopsNative(t *testing.T) {
	b := aiTestBrowser(t)
	for _, video := range []bool{false, true} {
		t.Run(fmt.Sprint(video), func(t *testing.T) {
			p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, "original_reset_later")
			ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
			defer cancel()
			var err error
			if video {
				err = submitPublishVideo(ctx, p.Context(ctx), "test", "test", nil, nil, true, "", nil, nil, ContentDeclarationOptions{"自主拍摄", testDeclarationDetails("自主拍摄")})
			} else {
				err = submitPublish(ctx, p.Context(ctx), "test", "test", nil, nil, true, "", nil, nil, ContentDeclarationOptions{"自主拍摄", testDeclarationDetails("自主拍摄")})
			}
			require.Error(t, err, "后续操作重置原创开关时必须在发布前停止")
			require.Zero(t, submits.Load())
		})
	}
}

func TestImageAndVideoPublishDeclarationsNative(t *testing.T) {
	b := aiTestBrowser(t)
	for _, video := range []bool{false, true} {
		for _, tc := range []struct {
			label, mode string
			orig, ok    bool
		}{
			{"自主拍摄", "normal", false, true}, {"来源转载", "not_retained", false, false}, {aiDeclarationLabel, "normal", true, true}, {aiDeclarationLabel, "original_not_retained", true, false}, {NoContentDeclaration, "normal", false, true},
		} {
			t.Run(fmt.Sprintf("video=%t/%s/%s", video, tc.label, tc.mode), func(t *testing.T) {
				p, submits, original, declared := declarationFixture(t, b, aiDeclarationLabel, tc.mode)
				ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
				defer cancel()
				var err error
				if video {
					err = submitPublishVideo(ctx, p.Context(ctx), "test", "test", nil, nil, tc.orig, "", nil, nil, ContentDeclarationOptions{tc.label, testDeclarationDetails(tc.label)})
				} else {
					err = submitPublish(ctx, p.Context(ctx), "test", "test", nil, nil, tc.orig, "", nil, nil, ContentDeclarationOptions{tc.label, testDeclarationDetails(tc.label)})
				}
				if tc.ok {
					require.NoError(t, err)
					require.EqualValues(t, 1, submits.Load())
					want := tc.label
					if want == NoContentDeclaration {
						want = ""
					}
					require.Equal(t, want, declared.Load())
					require.Equal(t, tc.orig, original.Load())
				} else {
					require.Error(t, err)
					require.Zero(t, submits.Load())
				}
			})
		}
	}
}

func testDeclarationDetails(label string) *ContentDeclarationDetails {
	if label == "自主拍摄" {
		return &ContentDeclarationDetails{ShootLocation: "Fixture Point", ShootDate: "2026-11-01"}
	}
	if label == "来源转载" {
		return &ContentDeclarationDetails{SourceMedia: "Fixture Media"}
	}
	return nil
}

func sourceDialogFixture(mode string) string {
	markup := fmt.Sprintf(`<style>#source-dialog{position:fixed;inset:0;z-index:100;background:white;padding:30px}#source-dialog input{display:block;padding:8px;margin:12px}.el-autocomplete-suggestion{position:fixed;top:88px;left:30px;z-index:110;background:#ddd;padding:20px}.d-datepicker-main{padding:12px;cursor:pointer}#source-dialog .d-modal-footer button{margin:20px}</style>
<div id="source-dialog" hidden><div class="d-modal-content"><div id="shoot-fields"><label>拍摄地点</label><input id="shoot-location" placeholder="下拉选择地点" oninput="document.getElementById('location-options').hidden=this.value!=='Fixture Point'"><label>拍摄日期</label><div class="d-datepicker-main" onclick="document.getElementById('date-wrapper').hidden=false">选择日期</div><div id="date-wrapper" class="d-datepicker-content" hidden><input id="shoot-date" placeholder="下拉选择日期" onkeydown="if(event.key==='Enter'){event.preventDefault();if(%t)this.value='';updateSourceConfirm()}"></div></div><div id="media-fields"><input id="source-media" placeholder="请输入媒体名称" oninput="if(%t)this.value='';updateSourceConfirm()"></div></div><div class="d-modal-footer"><button id="source-confirm" disabled onclick="if(!%t)document.getElementById('source-dialog').hidden=true">确认</button></div></div>
<ul id="location-options" class="el-autocomplete-suggestion" hidden><li onclick="selectFixtureLocation()">Fixture Point<br>Fixture address</li>%s</ul>
<script>let sourceKind='';let oldSourceChoose=choose;choose=function(v){let previous=document.getElementById('value').textContent;oldSourceChoose(v);if((v==='自主拍摄'||v==='来源转载')&&v!==previous){sourceKind=v;document.getElementById('shoot-fields').hidden=v!=='自主拍摄';document.getElementById('media-fields').hidden=v!=='来源转载';document.getElementById('source-dialog').hidden=false;document.getElementById('source-confirm').disabled=true;}}
function selectFixtureLocation(){document.getElementById('shoot-location').value='Fixture Point';document.getElementById('location-options').hidden=true;updateSourceConfirm()}
function updateSourceConfirm(){document.getElementById('source-confirm').disabled=sourceKind==='自主拍摄'?(!document.getElementById('shoot-location').value||!document.getElementById('shoot-date').value):!document.getElementById('source-media').value}</script>`, mode == "date_not_retained", mode == "media_not_retained", mode == "modal_not_closed", func() string {
		if mode == "location_ambiguous" {
			return `<li>Fixture Point<br>Other address</li>`
		}
		return ""
	}())
	if mode == "date_placeholder_removed" {
		markup += `<script>document.getElementById('shoot-date').addEventListener('keydown',function(e){if(e.key==='Enter')this.removeAttribute('placeholder')})</script>`
	}
	return markup
}

func TestSourceDetailsFailuresStopNative(t *testing.T) {
	b := aiTestBrowser(t)
	for _, tc := range []struct{ label, mode string }{{"自主拍摄", "location_ambiguous"}, {"自主拍摄", "date_not_retained"}, {"来源转载", "media_not_retained"}, {"来源转载", "modal_not_closed"}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			require.Error(t, setContentDeclaration(p.Context(ctx), tc.label, testDeclarationDetails(tc.label)))
			require.Zero(t, submits.Load())
		})
	}
}

func TestSameSourceChoiceUpdatesDetailsNative(t *testing.T) {
	b := aiTestBrowser(t)
	p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, "normal")
	require.NoError(t, setContentDeclaration(p, "来源转载", &ContentDeclarationDetails{SourceMedia: "First fixture"}))
	require.NoError(t, setContentDeclaration(p, "来源转载", &ContentDeclarationDetails{SourceMedia: "Second fixture"}))
	field, err := p.Element(`input[placeholder="请输入媒体名称"]`)
	require.NoError(t, err)
	value, err := field.Property("value")
	require.NoError(t, err)
	require.Equal(t, "Second fixture", value.String())
	require.Zero(t, submits.Load())
}

func TestShootDateSurvivesPlaceholderRemovalNative(t *testing.T) {
	b := aiTestBrowser(t)
	p, submits, _, _ := declarationFixture(t, b, aiDeclarationLabel, "date_placeholder_removed")
	require.NoError(t, setContentDeclaration(p, "自主拍摄", testDeclarationDetails("自主拍摄")))
	require.Zero(t, submits.Load())
}
