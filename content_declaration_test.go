package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func declarationMCPSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := InitMCPServer(&AppServer{xiaohongshuService: &XiaohongshuService{}})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "declaration-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func TestContentDeclarationAdvertisedSchema(t *testing.T) {
	session := declarationMCPSession(t)
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range result.Tools {
		if tool.Name != "publish_content" && tool.Name != "publish_with_video" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var schema map[string]any
		require.NoError(t, json.Unmarshal(raw, &schema))
		props := schema["properties"].(map[string]any)
		require.Contains(t, props, "declaration_details")
		require.Contains(t, props, "content_declaration")
		declaration := props["content_declaration"].(map[string]any)
		require.Equal(t, "string", declaration["type"])
		for _, label := range []string{"笔记含AI合成内容", "自主拍摄", "来源转载", "虚构演绎，仅供娱乐", "内容包含营销广告", "无需声明"} {
			require.Contains(t, declaration["enum"], label)
		}
		require.Equal(t, true, props["ai_generated"].(map[string]any)["default"])
		if tool.Name == "publish_with_video" {
			require.Contains(t, props, "is_original")
		}
	}
}

func TestMCPContentDeclarationKeepsCallerExplicitness(t *testing.T) {
	session := declarationMCPSession(t)
	for _, tc := range []struct {
		name, label string
		ai          *bool
		want        string
	}{
		{"default", "", nil, "视频文件不存在"},
		{"explicit_AI", "笔记含AI合成内容", nil, "视频文件不存在"},
		{"other_without_AI", "自主拍摄", nil, "视频文件不存在"},
		{"no_declaration", "无需声明", nil, "视频文件不存在"},
		{"AI_conflict", "来源转载", boolPtr(true), "冲突"},
		{"false_AI_conflict", "笔记含AI合成内容", boolPtr(false), "冲突"},
		{"false_other", "来源转载", boolPtr(false), "视频文件不存在"},
		{"invalid", "不存在的声明", nil, "content_declaration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"title": "test", "content": "test", "video": "/definitely-missing-declaration-regression.mp4"}
			if tc.label != "" {
				args["content_declaration"] = tc.label
			}
			if tc.label == "自主拍摄" {
				args["declaration_details"] = map[string]any{"shoot_location": "Fixture Point", "shoot_date": "2026-11-01"}
			} else if tc.label == "来源转载" {
				args["declaration_details"] = map[string]any{"source_media": "Fixture Media"}
			}
			if tc.ai != nil {
				args["ai_generated"] = *tc.ai
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish_with_video", Arguments: args})
			require.NoError(t, err)
			var parts []string
			for _, item := range result.Content {
				if text, ok := item.(*mcp.TextContent); ok {
					parts = append(parts, text.Text)
				}
			}
			require.Contains(t, strings.Join(parts, "\n"), tc.want)
			if tc.name != "invalid" {
				require.Equal(t, "failed", result.StructuredContent.(map[string]any)["status"])
			}
		})
	}
}

func TestServiceRejectsInvalidDeclarationBeforeMediaProcessing(t *testing.T) {
	for _, video := range []bool{false, true} {
		raw, marshalErr := json.Marshal(map[string]any{"title": strings.Repeat("测", 21), "content": "test", "images": []string{"/missing.png"}, "video": "/missing.mp4", "ai_generated": true, "content_declaration": "来源转载"})
		require.NoError(t, marshalErr)
		service := &XiaohongshuService{}
		var err error
		if video {
			var req PublishVideoRequest
			require.NoError(t, json.Unmarshal(raw, &req))
			_, err = service.PublishVideo(context.Background(), &req)
		} else {
			var req PublishRequest
			require.NoError(t, json.Unmarshal(raw, &req))
			_, err = service.PublishContent(context.Background(), &req)
		}
		require.ErrorContains(t, err, "冲突")
	}
}

func TestServiceRejectsMissingSourceDetailsBeforeMediaProcessing(t *testing.T) {
	for _, video := range []bool{false, true} {
		for _, label := range []string{"自主拍摄", "来源转载"} {
			raw, err := json.Marshal(map[string]any{"title": strings.Repeat("测", 21), "content": "test", "images": []string{"/missing.png"}, "video": "/missing.mp4", "content_declaration": label})
			require.NoError(t, err)
			service := &XiaohongshuService{}
			if video {
				var req PublishVideoRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				_, err = service.PublishVideo(context.Background(), &req)
			} else {
				var req PublishRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				_, err = service.PublishContent(context.Background(), &req)
			}
			require.ErrorContains(t, err, "declaration_details")
		}
	}
}
