package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestPublishToolStructuredOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status   string
		err            error
		retry, isError bool
	}{
		{"submitted", "submitted", nil, false, false},
		{"pre_submit", "failed", errors.New("AI 声明未确认，已停止发布"), true, true},
		{"after_click", "unknown", fmt.Errorf("wrapped: %w", xiaohongshu.ErrPublishResultUnknown), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := convertToMCPResult(publishToolResult(nil, tc.err))
			state := result.StructuredContent.(map[string]any)
			require.Equal(t, tc.status, state["status"])
			require.Equal(t, tc.retry, state["retry_safe"])
			require.Equal(t, tc.isError, result.IsError)
			if tc.status == "failed" {
				require.Equal(t, "pre_submit", state["phase"])
			}
		})
	}
}

func TestRESTPublishUnknownIsNotRetryableFailure(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/publish", nil)
	require.True(t, respondPublishUnknown(c, xiaohongshu.ErrPublishResultUnknown))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), `"retry_safe":false`)
	require.Contains(t, w.Body.String(), "PUBLISH_RESULT_UNKNOWN")
	require.False(t, respondPublishUnknown(c, errors.New("pre-submit")))
}
