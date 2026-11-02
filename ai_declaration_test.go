package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// 防止任一发布工具遗漏可选 AI 声明参数，或迫使旧调用方提供新参数。
func TestPublishToolsExposeOptionalAIGenerated(t *testing.T) {
	ctx := context.Background()
	server := InitMCPServer(&AppServer{})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "ai-schema-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	tools, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	found := 0
	for _, tool := range tools.Tools {
		if tool.Name != "publish_content" && tool.Name != "publish_with_video" {
			continue
		}
		found++
		t.Run(tool.Name, func(t *testing.T) {
			data, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			require.NoError(t, json.Unmarshal(data, &schema))
			_, hasAI := schema.Properties["ai_generated"]
			require.True(t, hasAI, "发布工具必须提供 ai_generated")
			var ai struct {
				Type    string `json:"type"`
				Default *bool  `json:"default"`
			}
			require.NoError(t, json.Unmarshal(schema.Properties["ai_generated"], &ai))
			require.Equal(t, "boolean", ai.Type)
			require.NotNil(t, ai.Default, "省略参数时必须默认开启")
			require.True(t, *ai.Default)
			require.NotContains(t, schema.Required, "ai_generated")
		})
	}
	require.Equal(t, 2, found)
}

// MCP 类型化参数与原 map 调用都必须保留显式 false，不能变成缺省开启。
func TestAIGeneratedArgumentPreservesExplicitFalse(t *testing.T) {
	f, tr := false, true
	for _, tc := range []struct {
		name  string
		input map[string]interface{}
		want  *bool
	}{
		{"omitted", map[string]interface{}{}, nil},
		{"false", map[string]interface{}{"ai_generated": false}, &f},
		{"true", map[string]interface{}{"ai_generated": true}, &tr},
		{"pointer_false", map[string]interface{}{"ai_generated": &f}, &f},
		{"pointer_true", map[string]interface{}{"ai_generated": &tr}, &tr},
		{"pointer_nil", map[string]interface{}{"ai_generated": (*bool)(nil)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, parseAIGenerated(tc.input)) })
	}
}
