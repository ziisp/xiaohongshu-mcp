package xiaohongshu

import (
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const aiDeclarationLabel = "笔记含AI合成内容"
const declarationPlaceholder = "添加内容类型声明"

// 保留已有 AI 入口；全部交互均使用 go-rod 原生方法。
func setAIContentDeclaration(page *rod.Page) error {
	return setContentDeclaration(page, aiDeclarationLabel)
}

func setContentDeclaration(page *rod.Page, label string, details ...*ContentDeclarationDetails) error {
	effective, err := ResolveContentDeclaration(label, nil)
	if err != nil {
		return err
	}
	label = effective
	var detail *ContentDeclarationDetails
	if len(details) > 0 {
		detail = details[0]
	}
	if err := ValidateContentDeclarationDetails(label, detail); err != nil {
		return err
	}
	pp := page.Timeout(15 * time.Second)
	defer pp.CancelTimeout()
	control, current, err := findContentDeclaration(pp)
	if err != nil {
		return err
	}
	if (current == label && label != "自主拍摄" && label != "来源转载") || (label == NoContentDeclaration && current == "") {
		return nil
	}
	if current == label && (label == "自主拍摄" || label == "来源转载") {
		// 平台重复选同一项不触发来源弹窗。显式改来源信息时先原生清除，再重新填写。
		if err := setContentDeclaration(pp, NoContentDeclaration); err != nil {
			return errors.Wrap(err, "重新填写来源信息前清除旧声明失败")
		}
		control, _, err = findContentDeclaration(pp)
		if err != nil {
			return err
		}
	}
	if err := control.ScrollIntoView(); err != nil {
		return errors.Wrap(err, "滚动内容声明控件失败")
	}
	if label == NoContentDeclaration {
		// 真实平台清除图标在悬停后出现，不通过改写 DOM 伪造空值。
		if err := control.Hover(); err != nil {
			return errors.Wrap(err, "显示内容声明清除按钮失败")
		}
		parent, err := control.Parent()
		if err != nil {
			return err
		}
		clear, err := parent.Element(".d-select-suffix-clear")
		if err != nil {
			return errors.Wrap(err, "未找到内容声明清除按钮")
		}
		if err := clear.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return errors.Wrap(err, "清除内容声明失败")
		}
	} else {
		if err := humanize.Click(control); err != nil {
			return errors.Wrap(err, "打开内容声明下拉框失败")
		}
		if label == "自主拍摄" || label == "来源转载" {
			header, err := pp.ElementR(".d-option-name span", "^\\s*内容来源声明\\s*$")
			if err != nil {
				return errors.Wrap(err, "未找到内容来源子菜单")
			}
			if err := header.Hover(); err != nil {
				return errors.Wrap(err, "打开内容来源子菜单失败")
			}
		}
		option, err := pp.ElementR(".d-option-name span", "^\\s*"+regexp.QuoteMeta(label)+"\\s*$")
		if err != nil {
			return errors.Wrapf(err, "未找到平台内容声明选项 %q", label)
		}
		if err := option.WaitVisible(); err != nil {
			return errors.Wrap(err, "内容声明选项不可见")
		}
		if err := declarationOptionEnabled(option); err != nil {
			return err
		}
		if err := option.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return errors.Wrap(err, "选择内容声明失败")
		}
	}
	if label == "自主拍摄" || label == "来源转载" {
		if err := confirmContentDeclarationDetails(pp, label, detail); err != nil {
			return errors.Wrap(err, "内容来源必填弹窗未确认")
		}
	}
	if err := pp.Keyboard.Type(input.Escape); err != nil {
		return errors.Wrap(err, "关闭内容声明菜单失败")
	}
	for {
		// 选择后重新定位，避免 Vue 替换节点导致读取旧选中值。
		_, current, err = findContentDeclaration(pp)
		if err != nil {
			return err
		}
		if current == label || (label == NoContentDeclaration && current == "") {
			return nil
		}
		select {
		case <-pp.GetContext().Done():
			return errors.Wrap(pp.GetContext().Err(), "未确认内容声明选中状态")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func declarationOptionEnabled(option *rod.Element) error {
	node := option
	for i := 0; i < 4; i++ {
		parent, err := node.Parent()
		if err != nil {
			return err
		}
		node = parent
		disabled, err := node.Attribute("disabled")
		if err != nil {
			return err
		}
		aria, err := node.Attribute("aria-disabled")
		if err != nil {
			return err
		}
		class, err := node.Attribute("class")
		if err != nil {
			return err
		}
		if disabled != nil || (aria != nil && *aria == "true") || (class != nil && hasExactClass(*class, "disabled")) {
			return errors.New("内容声明选项不可用")
		}
		if class != nil && hasExactClass(*class, "d-option") {
			break
		}
	}
	return nil
}

func findContentDeclaration(page *rod.Page) (*rod.Element, string, error) {
	for {
		elems, err := page.Elements(".d-select-content")
		if err != nil {
			return nil, "", errors.Wrap(err, "查找内容声明下拉框失败")
		}
		for _, elem := range elems {
			visible, err := elem.Visible()
			if err != nil {
				return nil, "", err
			}
			if !visible {
				continue
			}
			value, err := declarationValue(elem)
			if err != nil {
				return nil, "", err
			}
			if value != "" {
				for _, choice := range ContentDeclarationChoices() {
					if choice != NoContentDeclaration && value == choice {
						return elem, value, nil
					}
				}
			}
			has, placeholder, err := elem.Has(".d-select-placeholder")
			if err != nil {
				return nil, "", err
			}
			if has {
				text, err := placeholder.Text()
				if err != nil {
					return nil, "", err
				}
				if strings.TrimSpace(text) == declarationPlaceholder && value == "" {
					return elem, "", nil
				}
			}
		}
		select {
		case <-page.GetContext().Done():
			return nil, "", errors.Wrap(page.GetContext().Err(), "未找到内容声明下拉框")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func declarationValue(control *rod.Element) (string, error) {
	has, value, err := control.Has(".d-select-description")
	if err != nil || !has {
		return "", err
	}
	text, err := value.Text()
	return strings.TrimSpace(text), err
}

func findAIContentDeclaration(page *rod.Page) (*rod.Element, bool, error) {
	elem, value, err := findContentDeclaration(page)
	return elem, value == aiDeclarationLabel, err
}

func aiDeclarationSelected(control *rod.Element) (bool, error) {
	value, err := declarationValue(control)
	return value == aiDeclarationLabel, err
}
