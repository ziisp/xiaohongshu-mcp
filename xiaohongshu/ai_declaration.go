package xiaohongshu

import (
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/pkg/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const aiDeclarationLabel = "笔记含AI合成内容"

// 原生查找、滚动、点击并回读选中项；不注入 JS，不用菜单文字冒充选中态。
func setAIContentDeclaration(page *rod.Page) error {
	pp := page.Timeout(8 * time.Second)
	defer pp.CancelTimeout()
	selectElem, selected, err := findAIContentDeclaration(pp)
	if err != nil {
		return err
	}
	if selected {
		return nil
	}
	if err := selectElem.ScrollIntoView(); err != nil {
		return errors.Wrap(err, "滚动内容声明选项失败")
	}
	if err := humanize.Click(selectElem); err != nil {
		return errors.Wrap(err, "打开内容声明下拉框失败")
	}
	option, err := pp.ElementR(".d-option-name span", "^\\s*"+aiDeclarationLabel+"\\s*$")
	if err != nil {
		return errors.Wrap(err, "未找到 AI 内容声明选项")
	}
	row, err := option.Parent()
	if err != nil {
		return errors.Wrap(err, "读取 AI 内容声明选项失败")
	}
	if isButtonDisabled(row) {
		return errors.New("AI 内容声明选项不可用")
	}
	ariaDisabled, err := row.Attribute("aria-disabled")
	if err != nil {
		return errors.Wrap(err, "读取 AI 内容声明选项状态失败")
	}
	if ariaDisabled != nil && *ariaDisabled == "true" {
		return errors.New("AI 内容声明选项不可用")
	}
	if err := humanize.Click(option); err != nil {
		return errors.Wrap(err, "选择 AI 内容声明失败")
	}
	if err := pp.Keyboard.Type(input.Escape); err != nil {
		return errors.Wrap(err, "关闭内容声明菜单失败")
	}
	for {
		selected, err = aiDeclarationSelected(selectElem)
		if err != nil {
			return err
		}
		if selected {
			return nil
		}
		select {
		case <-pp.GetContext().Done():
			return errors.Wrap(pp.GetContext().Err(), "未确认 AI 内容声明选中状态")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func findAIContentDeclaration(page *rod.Page) (*rod.Element, bool, error) {
	for {
		elems, err := page.Elements(".d-select-content")
		if err != nil {
			return nil, false, errors.Wrap(err, "查找内容声明下拉框失败")
		}
		for _, elem := range elems {
			visible, err := elem.Visible()
			if err != nil {
				return nil, false, err
			}
			if !visible {
				continue
			}
			selected, err := aiDeclarationSelected(elem)
			if err != nil {
				return nil, false, err
			}
			if selected {
				return elem, true, nil
			}
			has, placeholder, err := elem.Has(".d-select-placeholder")
			if err != nil {
				return nil, false, err
			}
			if has {
				text, err := placeholder.Text()
				if err != nil {
					return nil, false, err
				}
				if strings.TrimSpace(text) == "添加内容类型声明" {
					return elem, false, nil
				}
			}
		}
		select {
		case <-page.GetContext().Done():
			return nil, false, errors.Wrap(page.GetContext().Err(), "未找到内容声明下拉框")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// 仅回读控件值节点，不读取下拉菜单或其他控件的文本。
func aiDeclarationSelected(control *rod.Element) (bool, error) {
	has, value, err := control.Has(".d-select-description")
	if err != nil || !has {
		return false, err
	}
	text, err := value.Text()
	return strings.TrimSpace(text) == aiDeclarationLabel, err
}
