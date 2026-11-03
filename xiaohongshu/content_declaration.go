package xiaohongshu

import (
	"fmt"
	"strings"
	"time"
)

const NoContentDeclaration = "无需声明"

// ContentDeclarationDetails 对应平台内容来源弹窗的实际必填字段。
type ContentDeclarationDetails struct {
	ShootLocation string `json:"shoot_location,omitempty" jsonschema:"自主拍摄必填：平台地点联想中的准确地点名称，必须唯一匹配，不自动选其他地点"`
	ShootDate     string `json:"shoot_date,omitempty" jsonschema:"自主拍摄必填：真实拍摄日期，YYYY-MM-DD"`
	SourceMedia   string `json:"source_media,omitempty" jsonschema:"来源转载必填：真实媒体名称"`
}

type ContentDeclarationOptions struct {
	Label   string
	Details *ContentDeclarationDetails
}

func ValidateContentDeclarationDetails(label string, details *ContentDeclarationDetails) error {
	d := ContentDeclarationDetails{}
	if details != nil {
		d = *details
	}
	d.ShootLocation = strings.TrimSpace(d.ShootLocation)
	d.ShootDate = strings.TrimSpace(d.ShootDate)
	d.SourceMedia = strings.TrimSpace(d.SourceMedia)
	if label == "自主拍摄" {
		if strings.TrimSpace(d.ShootLocation) == "" || d.ShootDate == "" {
			return fmt.Errorf("自主拍摄需要 declaration_details.shoot_location（真实地点名称）和 shoot_date（YYYY-MM-DD），缺失时停止发布")
		}
		date, err := time.Parse("2006-01-02", d.ShootDate)
		if err != nil || date.Format("2006-01-02") != d.ShootDate {
			return fmt.Errorf("shoot_date 必须为有效日期 YYYY-MM-DD")
		}
		if d.SourceMedia != "" {
			return fmt.Errorf("自主拍摄不接受 source_media")
		}
	} else if label == "来源转载" {
		if strings.TrimSpace(d.SourceMedia) == "" {
			return fmt.Errorf("来源转载需要 declaration_details.source_media（真实媒体名称），缺失时停止发布")
		}
		if d.ShootLocation != "" || d.ShootDate != "" {
			return fmt.Errorf("来源转载不接受拍摄地点或日期")
		}
	} else if d != (ContentDeclarationDetails{}) {
		return fmt.Errorf("该内容声明不接受来源补充信息")
	}
	return nil
}

// 仅开放真实平台选项；内容来源是同一单选控件中的二级菜单。
func ContentDeclarationChoices() []string {
	return []string{aiDeclarationLabel, "自主拍摄", "来源转载", "虚构演绎，仅供娱乐", "内容包含营销广告", NoContentDeclaration}
}

// 显式选择优先于缺省 AI；显式相互矛盾的参数必须停止，旧 false 保持不修改控件。
func ResolveContentDeclaration(requested string, aiGenerated *bool) (string, error) {
	if requested == "" {
		if aiGenerated != nil && !*aiGenerated {
			return "", nil
		}
		return aiDeclarationLabel, nil
	}
	found := false
	for _, choice := range ContentDeclarationChoices() {
		if requested == choice {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("不支持的内容声明 %q，请使用平台实际选项", requested)
	}
	if aiGenerated != nil && ((*aiGenerated && requested != aiDeclarationLabel) || (!*aiGenerated && requested == aiDeclarationLabel)) {
		return "", fmt.Errorf("内容声明与 ai_generated 参数冲突，请只保留一致的明确选择")
	}
	return requested, nil
}
