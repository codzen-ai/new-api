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
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":0.5,"gpt-4o-mini":0.3},"svip":{"gpt-4o":0}}`)
	defer restore()

	cases := []struct {
		name      string
		group     string
		model     string
		wantRatio float64
		wantFound bool
	}{
		{"命中", "vip", "gpt-4o", 0.5, true},
		{"命中另一模型", "vip", "gpt-4o-mini", 0.3, true},
		{"倍率为0仍算命中", "svip", "gpt-4o", 0, true},
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
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":0.5}}`)
	defer restore()

	gm, ok := GetGroupModelRatioByGroup("vip")
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"gpt-4o": 0.5}, gm)

	// 返回的是副本，改动不影响内部状态
	gm["gpt-4o"] = 999
	again, _ := GetGroupModelRatioByGroup("vip")
	assert.Equal(t, 0.5, again["gpt-4o"])

	_, ok = GetGroupModelRatioByGroup("no-such-group")
	assert.False(t, ok)
}

// TestGetGroupModelRatioForUsableGroups 锁定定价接口的安全边界：
// 只返回用户可用分组的倍率，专属/私有分组的价格不得外泄。
func TestGetGroupModelRatioForUsableGroups(t *testing.T) {
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":0.5},"client_acme":{"gpt-4o":0.3}}`)
	defer restore()

	t.Run("私有分组不外泄", func(t *testing.T) {
		// 用户可用分组仅 default / vip，不含专属分组 client_acme
		usable := map[string]string{"default": "默认", "vip": "VIP"}
		got := GetGroupModelRatioForUsableGroups(usable)

		_, hasVip := got["vip"]
		_, hasAcme := got["client_acme"]
		assert.True(t, hasVip, "可用分组 vip 的倍率应返回")
		assert.False(t, hasAcme, "不可用的专属分组 client_acme 的价格不得外泄")
		assert.Equal(t, map[string]float64{"gpt-4o": 0.5}, got["vip"])
	})

	t.Run("专属分组用户可见自己分组", func(t *testing.T) {
		// client_acme 用户的可用分组含自身
		usable := map[string]string{"default": "默认", "client_acme": "Acme 专属"}
		got := GetGroupModelRatioForUsableGroups(usable)

		assert.Equal(t, map[string]float64{"gpt-4o": 0.3}, got["client_acme"])
		_, hasVip := got["vip"]
		assert.False(t, hasVip)
	})

	t.Run("匿名用户（无可用分组）不返回任何倍率", func(t *testing.T) {
		got := GetGroupModelRatioForUsableGroups(map[string]string{})
		assert.Empty(t, got)
	})
}

// TestResolveGroupModelGroupRatio 覆盖核心不变量：分组模型倍率就是该模型在该分组的分组倍率——
// 取代分组倍率、不与之叠乘，且只影响分组倍率（模型自身的定价方式不变）。
func TestResolveGroupModelGroupRatio(t *testing.T) {
	restore := setGroupModelRatio(t, `{"vip":{"gpt-4o":0.5},"svip":{"gpt-4o":0}}`)
	defer restore()

	t.Run("命中：取代分组倍率而非叠乘", func(t *testing.T) {
		// 基础分组倍率 0.8 应被取代，结果既不是 0.8 也不是 0.4
		groupRatio, overridden := ResolveGroupModelGroupRatio("vip", "gpt-4o", 0.8)
		assert.True(t, overridden)
		assert.Equal(t, 0.5, groupRatio)
	})

	t.Run("命中倍率为0：该分组免费", func(t *testing.T) {
		groupRatio, overridden := ResolveGroupModelGroupRatio("svip", "gpt-4o", 0.8)
		assert.True(t, overridden)
		assert.Equal(t, 0.0, groupRatio)
	})

	t.Run("未命中：回退基础分组倍率", func(t *testing.T) {
		groupRatio, overridden := ResolveGroupModelGroupRatio("vip", "claude-3-5-sonnet", 0.8)
		assert.False(t, overridden)
		assert.Equal(t, 0.8, groupRatio)
	})

	t.Run("未配置分组：回退", func(t *testing.T) {
		groupRatio, overridden := ResolveGroupModelGroupRatio("default", "gpt-4o", 0.9)
		assert.False(t, overridden)
		assert.Equal(t, 0.9, groupRatio)
	})
}

func TestCheckGroupModelRatio(t *testing.T) {
	t.Run("合法", func(t *testing.T) {
		require.NoError(t, CheckGroupModelRatio(`{"vip":{"gpt-4o":0.5,"gpt-4o-mini":0}}`))
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
