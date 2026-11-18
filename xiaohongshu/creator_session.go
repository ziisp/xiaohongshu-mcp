package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

const (
	creatorDomain      = "creator.xiaohongshu.com"
	creatorPublishPath = "/publish/publish"
	urlOfPublic        = "https://" + creatorDomain + creatorPublishPath + "?source=official"
	creatorPageTimeout = 35 * time.Second
)

// openCreatorPublishPage 只判断真实发布页是否可用，不点击、上传或提交。
// 创作页不一定使用同名 Cookie；等待控件出现也让页面有时间自动登录。
func openCreatorPublishPage(page *rod.Page, tabName string) error {
	pp := page.Timeout(creatorPageTimeout)
	defer pp.CancelTimeout()
	if err := pp.Navigate(urlOfPublic); err != nil {
		return fmt.Errorf("导航到创作发布页失败: %w", err)
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var login bool
	var inspectErr error
	for {
		atLogin, ready, err := creatorPublishPageState(pp, tabName)
		if err == nil {
			login, inspectErr = atLogin, nil
		} else if pp.GetContext().Err() == nil {
			inspectErr = err
		}
		if ready {
			return nil
		}
		select {
		case <-pp.GetContext().Done():
			if err := page.GetContext().Err(); errors.Is(err, context.Canceled) {
				return fmt.Errorf("创作发布页未就绪: %w", err)
			}
			if inspectErr != nil {
				return fmt.Errorf("检查创作发布页失败: %w", inspectErr)
			}
			if login {
				return errors.New("创作平台跳转到登录页，请重新登录创作平台")
			}
			return fmt.Errorf("创作发布页未就绪：未看到「%s」和上传区域", tabName)
		case <-ticker.C:
		}
	}
}

func creatorPublishPageState(page *rod.Page, tabName string) (login, ready bool, err error) {
	info, err := page.Info()
	if err != nil {
		return false, false, err
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return false, false, errors.New("无法识别创作发布页地址")
	}
	if u.Scheme != "https" || u.Host != creatorDomain {
		return false, false, nil
	}
	switch strings.TrimSuffix(u.Path, "/") {
	case "/login", "/new/login":
		return true, false, nil
	case creatorPublishPath:
		for _, selector := range []struct{ name, text string }{
			{"div.upload-content", ""},
			{"div.creator-tab", tabName},
		} {
			elements, err := page.Elements(selector.name)
			if err != nil {
				return false, false, err
			}
			found := false
			for _, el := range elements {
				if selector.text != "" {
					text, err := el.Text()
					if err != nil {
						return false, false, err
					}
					if strings.TrimSpace(text) != selector.text {
						continue
					}
				}
				visible, err := el.Visible()
				if err != nil {
					return false, false, err
				}
				if visible {
					found = true
					break
				}
			}
			if !found {
				return false, false, nil
			}
		}
		return false, true, nil
	default:
		return false, false, nil
	}
}
