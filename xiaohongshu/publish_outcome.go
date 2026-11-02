package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/go-rod/rod"
)

// ErrPublishResultUnknown 表示已进入点击阶段，不能据此重发。
var ErrPublishResultUnknown = errors.New("提交结果未知，请先检查创作平台笔记管理，不要自动重新发布")

func publishUnknown(cause error) error {
	return fmt.Errorf("%w: %v", ErrPublishResultUnknown, cause)
}

func isPublishSuccessURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "creator.xiaohongshu.com" &&
		u.User == nil && u.Path == "/publish/success" && u.RawPath == ""
}

// waitPublishSuccess 只确认已提交，不代表已公开或审核通过。仅使用原生页面信息。
func waitPublishSuccess(page *rod.Page, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(page.GetContext(), timeout)
	defer cancel()
	p := page.Context(ctx)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return publishUnknown(ctx.Err())
		}
		if info, err := p.Info(); err == nil && isPublishSuccessURL(info.URL) {
			return nil
		}
		select {
		case <-ctx.Done():
			return publishUnknown(ctx.Err())
		case <-ticker.C:
		}
	}
}
