# New API — 分组 (Group) 功能总结

## 一、分组概览

分组是一个轻量级的组织单元，以**字符串**形式存储在用户、Token、渠道三个维度上，贯穿**访问控制、渠道路由、计费倍率**三大核心流程。

```
用户 (User.Group)
  ↓ Token 可覆盖
使用组 (UsingGroup)
  ↓
┌────────────┬──────────────┬─────────────┐
│ 访问控制    │ 渠道路由      │ 计费倍率     │
│ Ability 表  │ Distributor  │ GroupRatio  │
└────────────┴──────────────┴─────────────┘
```

## 二、数据模型

### 三个维度的 Group 字段

| 表/模型 | 字段 | 类型 | 默认值 | 说明 |
|---------|------|------|--------|------|
| `User` | `Group` | varchar(64) | `"default"` | 用户所属分组，**一对一** |
| `Token` | `Group` | string | `""` | 可选覆盖，空字符串表示使用用户分组 |
| `Channel` | `Group` | varchar(64) | `"default"` | 逗号分隔的分组列表，表示该渠道服务哪些分组 |

### Ability 表 — 访问控制矩阵

```
Ability {
    Group     string   // 分组名
    Model     string   // 模型名
    ChannelId int      // 渠道 ID
    Enabled   bool     // 是否启用
    Priority  int64    // 优先级
    Weight    uint     // 权重（加权随机）
}
主键: (Group, Model, ChannelId)
```

当渠道创建/更新时，系统为 **Group × Model** 的每种组合生成 Ability 记录。

## 三、分组解析流程

请求进入时，分组通过以下流程确定：

```
1. 获取用户分组
   userGroup = User.Group        // 从 DB/Cache 获取

2. Token 分组覆盖 (middleware/auth.go)
   if Token.Group != "" {
       验证 Token.Group ∈ 用户可用分组
       usingGroup = Token.Group   // 覆盖
   } else {
       usingGroup = userGroup
   }

3. "auto" 分组处理 (middleware/distributor.go)
   if usingGroup == "auto" {
       遍历 autoGroups 列表
       找到第一个有 Ability 记录(group, model)的分组
       usingGroup = 找到的分组
   }

4. 写入 Context
   ContextKeyUserGroup  = userGroup    // 用户原始分组
   ContextKeyUsingGroup = usingGroup   // 实际使用的分组
```

## 四、分组与访问控制

分组通过 **Ability 表**控制模型访问权限：

```
用户请求模型 "gpt-4o"，usingGroup = "vip"
    ↓
查询 Ability 表: WHERE group="vip" AND model="gpt-4o" AND enabled=true
    ↓
返回可用渠道列表 [(channelId=1, priority=10, weight=5), ...]
    ↓
无结果 → 403 该分组无权使用此模型
有结果 → 按 priority + weight 选择渠道
```

**核心函数**：`IsChannelEnabledForGroupModel(group, model, channelID)`

## 五、分组与渠道路由

**渠道选择流程** (`middleware/distributor.go` → `service/channel_select.go`)：

1. 根据 `(usingGroup, model)` 查 Ability 表获取可用渠道
2. 按 Priority 分级，同级内按 Weight 加权随机
3. 渠道亲和 (Channel Affinity)：倾向复用同一渠道以优化连接

**渠道侧配置**：
```
Channel.Group = "default,vip,svip"   // 该渠道服务这三个分组
```
→ 渠道创建时为每个分组生成 Ability 记录

## 六、分组与计费

### 6.1 基础分组倍率 (GroupRatio)

```go
// 默认配置
defaultGroupRatio = {
    "default": 1.0,
    "vip":     1.0,
    "svip":    1.0,
}
```

使用：`quota = tokenQuota × modelRatio × groupRatio`

### 6.2 二级分组倍率 (GroupGroupRatio)

当用户分组 ≠ 使用分组时，可定义特殊倍率：

```go
// 结构: userGroup → usingGroup → ratio
groupGroupRatioMap = {
    "vip": {
        "default": 0.9,    // VIP 用户使用 default 组享 0.9 折
    },
}
```

**应用逻辑**：
```go
func GetUserGroupRatio(userGroup, usingGroup) float64 {
    if specialRatio, ok := GetGroupGroupRatio(userGroup, usingGroup); ok {
        return specialRatio       // 优先使用二级倍率
    }
    return GetGroupRatio(usingGroup)  // 回退到基础倍率
}
```

### 6.3 计费中的应用

```go
// service/quota.go
groupRatio := GetGroupRatio(relayInfo.UsingGroup)
actualGroupRatio := groupRatio

// 检查二级倍率覆盖
if userGroupRatio, ok := GetGroupGroupRatio(userGroup, usingGroup); ok {
    actualGroupRatio = userGroupRatio
}

finalQuota = (promptQuota + completionQuota) × actualGroupRatio
```

## 七、用户可用分组

### 默认可用分组

```go
userUsableGroups = {
    "default": "默认分组",
    "vip":     "vip分组",
}
```

### 分组特殊可用组 (GroupSpecialUsableGroup)

允许特定分组的用户访问额外分组或排除某些分组：

```go
groupSpecialUsableGroup = {
    "vip": {
        "append_1":   "vip_special_group",     // 追加可用分组
        "-:remove_1": "some_removed_group",     // 移除可用分组
    },
}
```

语法：
- `"append_xxx"` 或 `"+:xxx"` → 添加分组
- `"-:xxx"` → 移除分组

### Auto 分组

```go
autoGroups = ["default"]   // 默认仅含 default
```

当 Token 分组设为 `"auto"` 时，系统自动遍历 autoGroups 列表，选择第一个有对应模型 Ability 的分组。Token 还支持 `CrossGroupRetry`（跨组重试）仅在 auto 模式下生效。

## 八、分组管理 API

| 端点 | 权限 | 说明 |
|------|------|------|
| `GET /api/group/` | Admin | 列出所有已配置分组名 |
| `GET /api/user/groups` | User | 获取当前用户可用分组（含倍率和描述）|

**GetUserGroups 响应格式**：
```json
{
  "default": { "ratio": 1.0, "desc": "默认分组" },
  "vip":     { "ratio": 0.9, "desc": "vip分组" }
}
```

分组本身**没有独立的 CRUD 管理**——通过修改系统设置中的 groupRatio、userUsableGroups 等配置来管理分组列表。

## 九、分组在请求生命周期中的位置

```
HTTP 请求
    ↓
TokenAuth 中间件
    → 解析用户分组 (User.Group)
    → Token 分组覆盖 (Token.Group)
    → 设置 usingGroup
    ↓
Distribute 中间件
    → "auto" 分组自动选择
    → 查询 Ability(group, model) 获取可用渠道
    → 按 priority/weight 选择渠道
    ↓
Controller (relay.go)
    → GenRelayInfo() 记录 UserGroup + UsingGroup
    → ModelPriceHelper() 计算 groupRatio
    → PreConsumeBilling() 预扣 = 估算 × groupRatio
    ↓
Adaptor 执行上游请求
    ↓
SettleBilling()
    → 实际 quota = actualTokens × modelRatio × groupRatio
    → 多退少补
    ↓
RecordConsumeLog()
    → 记录 groupRatio 到日志
```

## 十、关键文件索引

| 文件 | 职责 |
|------|------|
| `model/user.go` | User.Group 字段、缓存 |
| `model/token.go` | Token.Group 覆盖、CrossGroupRetry |
| `model/channel.go` | Channel.Group 多分组配置 |
| `model/ability.go` | Group×Model×Channel 访问矩阵 |
| `setting/ratio_setting/group_ratio.go` | 分组倍率、二级倍率、特殊可用组 |
| `setting/user_usable_group.go` | 用户可用分组列表 |
| `setting/auto_group.go` | Auto 分组列表 |
| `middleware/auth.go` | 分组解析与 Token 覆盖 |
| `middleware/distributor.go` | Auto 分组、渠道选择 |
| `service/group.go` | 分组工具函数 |
| `service/quota.go` | 分组倍率应用于计费 |
| `controller/group.go` | 分组管理 API |
