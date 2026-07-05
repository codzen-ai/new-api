package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setGroupModelRatio 在测试内显式设置分组模型倍率状态，并返回恢复函数。
func setGroupModelRatio(t *testing.T, jsonStr string) func() {
	t.Helper()
	original := GroupModelRatio2JSONString()
	require.NoError(t, UpdateGroupModelRatioByJSONString(jsonStr))
	return func() {
		require.NoError(t, UpdateGroupModelRatioByJSONString(original))
	}
}

func TestGetGroupModelRatio(t *testing.T) {
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":5,"gpt-4o-mini":0.3},"svip":{"gpt-4o":0}}`)
	defer restore()

	cases := []struct {
		name      string
		group     string
		model     string
		wantRatio float64
		wantFound bool
	}{
		{"命中覆盖", "vip", "gpt-4o", 5, true},
		{"命中另一模型", "vip", "gpt-4o-mini", 0.3, true},
		{"覆盖为0仍算命中", "svip", "gpt-4o", 0, true},
		{"分组内未配置的模型", "vip", "claude-3-5-sonnet", -1, false},
		{"未配置的分组", "default", "gpt-4o", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ratio, ok := GetGroupModelRatio(tc.group, tc.model)
			assert.Equal(t, tc.wantFound, ok)
			assert.Equal(t, tc.wantRatio, ratio)
		})
	}
}

func TestGetGroupModelRatioByGroup(t *testing.T) {
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":5}}`)
	defer restore()

	gm, ok := GetGroupModelRatioByGroup("vip")
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"gpt-4o": 5}, gm)

	// 返回的是副本，改动不影响内部状态
	gm["gpt-4o"] = 999
	again, _ := GetGroupModelRatioByGroup("vip")
	assert.Equal(t, 5.0, again["gpt-4o"])

	_, ok = GetGroupModelRatioByGroup("no-such-group")
	assert.False(t, ok)
}

// TestResolveGroupModelPrice 覆盖「覆盖即最终价」的核心不变量。
func TestResolveGroupModelPrice(t *testing.T) {
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":5},"svip":{"gpt-4o":0}}`)
	defer restore()

	t.Run("命中：覆盖即最终价，分组倍率归一为1.0", func(t *testing.T) {
		// 基础分组倍率 0.8 应被忽略
		modelRatio, groupRatio, overridden := ResolveGroupModelPrice("vip", "gpt-4o", 10, 0.8)
		assert.True(t, overridden)
		assert.Equal(t, 5.0, modelRatio)
		assert.Equal(t, 1.0, groupRatio)
	})

	t.Run("命中覆盖为0", func(t *testing.T) {
		modelRatio, groupRatio, overridden := ResolveGroupModelPrice("svip", "gpt-4o", 10, 0.8)
		assert.True(t, overridden)
		assert.Equal(t, 0.0, modelRatio)
		assert.Equal(t, 1.0, groupRatio)
	})

	t.Run("未命中：回退基础倍率×基础分组倍率", func(t *testing.T) {
		modelRatio, groupRatio, overridden := ResolveGroupModelPrice("vip", "claude-3-5-sonnet", 12, 0.8)
		assert.False(t, overridden)
		assert.Equal(t, 12.0, modelRatio)
		assert.Equal(t, 0.8, groupRatio)
	})

	t.Run("未配置分组：回退", func(t *testing.T) {
		modelRatio, groupRatio, overridden := ResolveGroupModelPrice("default", "gpt-4o", 10, 0.9)
		assert.False(t, overridden)
		assert.Equal(t, 10.0, modelRatio)
		assert.Equal(t, 0.9, groupRatio)
	})
}

func TestCheckGroupModelRatio(t *testing.T) {
	t.Run("合法", func(t *testing.T) {
		require.NoError(t, CheckGroupModelRatio(`{"vip":{"gpt-4o":5,"gpt-4o-mini":0}}`))
	})
	t.Run("负倍率报错", func(t *testing.T) {
		err := CheckGroupModelRatio(`{"vip":{"gpt-4o":-1}}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vip/gpt-4o")
	})
	t.Run("非法JSON报错", func(t *testing.T) {
		require.Error(t, CheckGroupModelRatio(`{not json`))
	})
}
