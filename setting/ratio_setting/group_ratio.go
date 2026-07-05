package ratio_setting

import (
	"encoding/json"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

var defaultGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}

var groupRatioMap = types.NewRWMap[string, float64]()

var defaultGroupGroupRatio = map[string]map[string]float64{
	"vip": {
		"edit_this": 0.9,
	},
}

var groupGroupRatioMap = types.NewRWMap[string, map[string]float64]()

// 分组模型倍率：GroupModelRatio[group][model] = ratio
// 语义（覆盖即最终价）：命中即为该分组下该模型的最终倍率，分组倍率对其失效。
var defaultGroupModelRatio = map[string]map[string]float64{}

var groupModelRatioMap = types.NewRWMap[string, map[string]float64]()

var defaultGroupSpecialUsableGroup = map[string]map[string]string{}

type GroupRatioSetting struct {
	GroupRatio              *types.RWMap[string, float64]            `json:"group_ratio"`
	GroupGroupRatio         *types.RWMap[string, map[string]float64] `json:"group_group_ratio"`
	GroupModelRatio         *types.RWMap[string, map[string]float64] `json:"group_model_ratio"`
	GroupSpecialUsableGroup *types.RWMap[string, map[string]string]  `json:"group_special_usable_group"`
}

var groupRatioSetting GroupRatioSetting

func init() {
	groupSpecialUsableGroup := types.NewRWMap[string, map[string]string]()
	groupSpecialUsableGroup.AddAll(defaultGroupSpecialUsableGroup)

	groupRatioMap.AddAll(defaultGroupRatio)
	groupGroupRatioMap.AddAll(defaultGroupGroupRatio)
	groupModelRatioMap.AddAll(defaultGroupModelRatio)

	groupRatioSetting = GroupRatioSetting{
		GroupSpecialUsableGroup: groupSpecialUsableGroup,
		GroupRatio:              groupRatioMap,
		GroupGroupRatio:         groupGroupRatioMap,
		GroupModelRatio:         groupModelRatioMap,
	}

	config.GlobalConfig.Register("group_ratio_setting", &groupRatioSetting)
}

func GetGroupRatioSetting() *GroupRatioSetting {
	if groupRatioSetting.GroupSpecialUsableGroup == nil {
		groupRatioSetting.GroupSpecialUsableGroup = types.NewRWMap[string, map[string]string]()
		groupRatioSetting.GroupSpecialUsableGroup.AddAll(defaultGroupSpecialUsableGroup)
	}
	return &groupRatioSetting
}

func GetGroupRatioCopy() map[string]float64 {
	return groupRatioMap.ReadAll()
}

func ContainsGroupRatio(name string) bool {
	_, ok := groupRatioMap.Get(name)
	return ok
}

func GroupRatio2JSONString() string {
	return groupRatioMap.MarshalJSONString()
}

func UpdateGroupRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupRatioMap, jsonStr)
}

func GetGroupRatio(name string) float64 {
	ratio, ok := groupRatioMap.Get(name)
	if !ok {
		common.SysLog("group ratio not found: " + name)
		return 1
	}
	return ratio
}

func GetGroupGroupRatio(userGroup, usingGroup string) (float64, bool) {
	gp, ok := groupGroupRatioMap.Get(userGroup)
	if !ok {
		return -1, false
	}
	ratio, ok := gp[usingGroup]
	if !ok {
		return -1, false
	}
	return ratio, true
}

func GroupGroupRatio2JSONString() string {
	return groupGroupRatioMap.MarshalJSONString()
}

func UpdateGroupGroupRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupGroupRatioMap, jsonStr)
}

// GetGroupModelRatio 返回某分组下某模型的模型倍率覆盖；不存在返回 (-1, false)。
func GetGroupModelRatio(group, model string) (float64, bool) {
	gm, ok := groupModelRatioMap.Get(group)
	if !ok {
		return -1, false
	}
	ratio, ok := gm[model]
	if !ok {
		return -1, false
	}
	return ratio, true
}

// GetGroupModelRatioByGroup 返回某分组下的模型倍率覆盖表副本；无覆盖返回 (nil, false)。
func GetGroupModelRatioByGroup(group string) (map[string]float64, bool) {
	gm, ok := groupModelRatioMap.Get(group)
	if !ok || len(gm) == 0 {
		return nil, false
	}
	out := make(map[string]float64, len(gm))
	for m, r := range gm {
		out[m] = r
	}
	return out, true
}

// ResolveGroupModelPrice 应用「覆盖即最终价」语义。
// 命中分组模型倍率 → (override, 1.0, true)：覆盖值为最终模型倍率，分组倍率归一为 1.0。
// 未命中 → (baseModelRatio, baseGroupRatio, false)：回退全局倍率 × 分组倍率。
func ResolveGroupModelPrice(usingGroup, model string, baseModelRatio, baseGroupRatio float64) (modelRatio float64, groupRatio float64, overridden bool) {
	if override, ok := GetGroupModelRatio(usingGroup, model); ok {
		return override, 1.0, true
	}
	return baseModelRatio, baseGroupRatio, false
}

func GroupModelRatio2JSONString() string {
	return groupModelRatioMap.MarshalJSONString()
}

func UpdateGroupModelRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupModelRatioMap, jsonStr)
}

// CheckGroupModelRatio 校验 JSON 合法且所有倍率非负。
func CheckGroupModelRatio(jsonStr string) error {
	check := make(map[string]map[string]float64)
	if err := json.Unmarshal([]byte(jsonStr), &check); err != nil {
		return err
	}
	for group, models := range check {
		for modelName, ratio := range models {
			if ratio < 0 {
				return errors.New("group model ratio must be not less than 0: " + group + "/" + modelName)
			}
		}
	}
	return nil
}

func CheckGroupRatio(jsonStr string) error {
	checkGroupRatio := make(map[string]float64)
	err := json.Unmarshal([]byte(jsonStr), &checkGroupRatio)
	if err != nil {
		return err
	}
	for name, ratio := range checkGroupRatio {
		if ratio < 0 {
			return errors.New("group ratio must be not less than 0: " + name)
		}
	}
	return nil
}
