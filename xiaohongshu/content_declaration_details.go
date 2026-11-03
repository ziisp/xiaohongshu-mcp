package xiaohongshu

import (
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

func confirmContentDeclarationDetails(page *rod.Page, label string, details *ContentDeclarationDetails) error {
	placeholder := "请输入媒体名称"
	if label == "自主拍摄" {
		placeholder = "下拉选择地点"
	}
	var modal *rod.Element
	for modal == nil {
		items, err := page.Elements(".d-modal-content")
		if err != nil {
			return err
		}
		for _, item := range items {
			visible, err := item.Visible()
			if err != nil {
				return err
			}
			has, _, err := item.Has(`input[placeholder="` + placeholder + `"]`)
			if err != nil {
				return err
			}
			if visible && has {
				modal = item
				break
			}
		}
		if modal == nil {
			select {
			case <-page.GetContext().Done():
				return errors.Wrap(page.GetContext().Err(), "未找到平台内容来源弹窗")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if label == "自主拍摄" {
		if err := setDeclarationShootLocation(page, modal, strings.TrimSpace(details.ShootLocation)); err != nil {
			return errors.Wrap(err, "选择拍摄地点失败")
		}
		date, err := modal.Element(".d-datepicker-main")
		if err != nil {
			return errors.Wrap(err, "查找拍摄日期控件失败")
		}
		if err := date.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return errors.Wrap(err, "打开拍摄日期失败")
		}
		// 平台在输入日期后移除 placeholder，编辑与回读均使用日期组件内的稳定定位。
		field, err := modal.Element(".d-datepicker-content input")
		if err != nil {
			return errors.Wrap(err, "查找拍摄日期输入框失败")
		}
		if err := replaceDeclarationInput(field, strings.TrimSpace(details.ShootDate)); err != nil {
			return errors.Wrap(err, "输入拍摄日期失败")
		}
		if err := page.Keyboard.Type(input.Enter); err != nil {
			return err
		}
		if err := verifyDeclarationInput(modal, "下拉选择日期", strings.TrimSpace(details.ShootDate)); err != nil {
			return errors.Wrap(err, "回读拍摄日期失败")
		}
	} else {
		field, err := modal.Element(`input[placeholder="请输入媒体名称"]`)
		if err != nil {
			return err
		}
		if err := replaceDeclarationInput(field, strings.TrimSpace(details.SourceMedia)); err != nil {
			return err
		}
		if err := verifyDeclarationInput(modal, "请输入媒体名称", strings.TrimSpace(details.SourceMedia)); err != nil {
			return err
		}
	}
	// 只在该来源弹窗的祖先中寻找确认按钮，不点击其他对话框。
	container := modal
	var footer *rod.Element
	for i := 0; i < 4; i++ {
		has, found, err := container.Has(".d-modal-footer")
		if err != nil {
			return errors.Wrap(err, "查找来源确认区域失败")
		}
		if has {
			footer = found
			break
		}
		container, err = container.Parent()
		if err != nil {
			return errors.Wrap(err, "定位来源弹窗父元素失败")
		}
	}
	if footer == nil {
		return errors.New("未找到内容来源弹窗确认区域")
	}
	confirm, err := footer.ElementR("button", `^\s*确认\s*$`)
	if err != nil {
		return errors.Wrap(err, "查找来源确认按钮失败")
	}
	if err := confirm.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "确认内容来源信息失败")
	}
	if err := modal.WaitInvisible(); err != nil {
		return errors.Wrap(err, "内容来源弹窗未关闭，已停止发布")
	}
	return nil
}

func replaceDeclarationInput(field *rod.Element, value string) error {
	if err := field.SelectAllText(); err != nil {
		return err
	}
	return humanize.Type(field.Page().GetContext(), field, value)
}

func verifyDeclarationInput(modal *rod.Element, placeholder, want string) error {
	selector := `input[placeholder="` + placeholder + `"]`
	if placeholder == "下拉选择日期" {
		selector = ".d-datepicker-content input"
	}
	field, err := modal.Element(selector)
	if err != nil {
		return err
	}
	value, err := field.Property("value")
	if err != nil {
		return err
	}
	if strings.TrimSpace(value.String()) != want {
		return errors.New("平台未保留内容来源填写值，已停止发布")
	}
	return nil
}

func setDeclarationShootLocation(page *rod.Page, modal *rod.Element, want string) error {
	field, err := modal.Element(`input[placeholder="下拉选择地点"]`)
	if err != nil {
		return errors.Wrap(err, "查找拍摄地点输入框失败")
	}
	if err := replaceDeclarationInput(field, want); err != nil {
		return errors.Wrap(err, "填写拍摄地点失败")
	}
	for {
		items, err := page.Elements(".el-autocomplete-suggestion li")
		if err != nil {
			return err
		}
		var matches []*rod.Element
		for _, item := range items {
			visible, err := item.Visible()
			if err != nil {
				return errors.Wrap(err, "读取地点候选可见性失败")
			}
			if !visible {
				continue
			}
			text, err := item.Text()
			if err != nil {
				return errors.Wrap(err, "读取地点候选名称失败")
			}
			if strings.TrimSpace(strings.Split(text, "\n")[0]) == want {
				matches = append(matches, item)
			}
		}
		if len(matches) > 1 {
			return errors.New("拍摄地点不唯一，请提供更准确的地点名称；已停止发布")
		}
		if len(matches) == 1 {
			if err := matches[0].Click(proto.InputMouseButtonLeft, 1); err != nil {
				return errors.Wrap(err, "选择准确地点失败")
			}
			return verifyDeclarationInput(modal, "下拉选择地点", want)
		}
		select {
		case <-page.GetContext().Done():
			return errors.Wrap(page.GetContext().Err(), "未找到准确的拍摄地点，已停止发布")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
