# moon-uestc Go SDK

「晚安成电」学生端的同步 Go 客户端。只用标准库 `net/http`、`encoding/json`、`context`，没有第三方依赖。覆盖学生本人接口，以及学生身份下的班委接口；不含辅导员、学院、校级管理接口。

契约与 Python SDK 共用，来源是 2026-10-22 部署的 H5 前端和本人账号的只读响应，不是官方公开 API。根目录 [README](../README.md) 包含完整的认证分析和接口背景。

## 模块路径

当前 `go.mod` 的 module 是 `moon-uestc/go-sdk`，用于本仓库和本地 `replace`。发布到代码托管前，把 module 改成实际仓库地址（例如 `github.com/<owner>/moon-uestc/go-sdk`），并同步所有 import；未带域名的 module 路径不能被 `go get` 从远端解析。

要求 Go 1.23+。

```go
import moonuestc "moon-uestc/go-sdk"
```

## 快速开始

只将自己的 `SESSION` Cookie 放入环境变量 `MOON_SESSION`。它是 HttpOnly Cookie，`document.cookie` 读不到，可在已登录页面的 Chrome DevTools → Application → Cookies → `https://jzsz.uestc.edu.cn` 查看。不要把 Cookie、票据或真实响应提交到仓库。

```go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	moonuestc "moon-uestc/go-sdk"
)

func main() {
	client, err := moonuestc.NewClient(moonuestc.Options{
		Session: os.Getenv("MOON_SESSION"),
	})
	if err != nil {
		panic(err)
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := client.Auth.UserInfo(ctx, "")
	if err != nil {
		panic(err)
	}
	fmt.Println(user.Data.UserData.FullName, user.Data.UserData.Role)

	record, err := client.Student.Today(ctx)
	if err != nil {
		panic(err)
	}
	if record.Data != nil {
		fmt.Println(record.Data.BusinessDate, record.Data.RecordStatusCode)
		for _, contact := range record.Data.InstructorList {
			fmt.Println(contact.Name) // JSON-in-a-string 已解码为联系人切片
		}
	}

	history, err := client.Student.History(ctx, moonuestc.HistoryQuery{
		Year: 2026, Month: 10, PageIndex: 1, PageSize: 10,
	})
	if err != nil {
		panic(err)
	}
	for _, item := range history.Data {
		fmt.Println(item.BusinessDate, item.RecordStatusCode)
	}
	if history.Page != nil {
		fmt.Println(history.Page.TotalCount, history.Page.TotalPage)
	}
}
```

所有 JSON 接口返回 `*Response[T]`：业务失败直接返回 `error`；成功时 `Data` 是已校验的领域类型，分页元信息在 `Page`，业务码在 `Code`。`DownloadAvatar` 返回原始 `[]byte`。客户端持有连接池，使用完调用 `CloseIdleConnections`；它没有需要关闭的后台 goroutine。

## 认证与授权

### 登录身份：SESSION

- 当前服务真正使用的是 `SESSION` Cookie。Go 客户端使用独立的域限制 cookiejar；普通 API 固定发送 `platform: h5`，仅显式票据获取请求发送 `platform: mp-weixin`。服务端对 SESSION 的更新由 cookiejar 保存；重定向仍然禁用。
- 匿名请求真实返回业务码 `5000`（未登录或会话已失效）；Go 集成测试已验证该映射为 `*AuthenticationError`。
- 登录响应中的 `accessToken`、`refreshToken` 目前都是 `NOT_IN_USE`，不能作为 Bearer 凭据。`Options.BearerToken` 仅保留前端存在的 Bearer 分支；未验证它可独立认证。
- `Auth.UserInfo(ctx, sessionID)` 可传 URL 入口的 `sessionId`；空字符串不发送该参数。该换取会话的分支未取得有效样本。
- 未发现学生前端的密码登录或刷新 token 接口。Go SDK 不自动登录、不续期、不重试，当前没有显式 CAS 换 SESSION 方法；Python 的 `auth.exchange_session()` 可用自己的有效统一认证 Cookie 换取 SESSION，详见根目录 README。
- 禁止重定向：`NewClient` 会覆盖调用方 `HTTPClient` 的 `CheckRedirect`，避免 Cookie 被带到其他主机。仍应始终使用 HTTPS。

### 打卡与情况填报：独立操作票据

`Student.Checkin` 和 `Student.Report` 需要独立的 `X-Checkin-Ticket`，它与登录 Cookie 是两套授权。可以显式调用 `Auth.RequestCheckinTicket(ctx, code)`，用调用方在官方目标 AppID 的实际小程序运行时执行 `uni.login({provider: "weixin"})` 获得的 `code` 换取票据；仅有解包的静态 bundle 无法取得该 code。SDK 不调用 `uni.login`。

```go
client, err := moonuestc.NewClient(moonuestc.Options{
	Session: os.Getenv("MOON_SESSION"),
})
if err != nil {
	panic(err)
}

loginCode := os.Getenv("MOON_WECHAT_LOGIN_CODE") // 由调用方从 uni.login(...) 获得
ticketResponse, err := client.Auth.RequestCheckinTicket(ctx, loginCode)
if err != nil {
	panic(err)
}
// 获取不会自动安装或替换客户端中的票据；显式安装后才可用于打卡和填报。
if err := client.SetCheckinTicket(ticketResponse.Data.Ticket); err != nil {
	panic(err)
}
```

- 请求为 `POST /platform-app/api/auth/checkin-ticket`，使用 `platform: mp-weixin` 和同一客户端的 `SESSION` Cookie。路径从 `BaseURL` 的 origin（scheme + host）开始，而非追加到 `BaseURL` 的路径前缀：例如 `BaseURL: "https://example.test/daily-checkin"` 时，票据请求发往 `https://example.test/platform-app/api/auth/checkin-ticket`，普通 API 仍使用 `/daily-checkin/...` 并发送 `platform: h5`。
- `code` 只校验非空且原样发送，不会 trim 或额外校验其格式。成功必须满足原始响应字段 `ok: true`；`code: 0` 或 `status: true` 不能代替它。返回的 `data.ticket` 必须恰为 43 个 ASCII 字符 `[A-Za-z0-9_-]`。
- 获取结果只返回在 `*Response[CheckinTicket]` 中，不会自动安装、替换或清除已配置的票据；获取失败也不会改动已有票据。可用 `SetCheckinTicket(ticketResponse.Data.Ticket)` 显式安装，或使用 `Options.CheckinTicket` 预先配置；可用 `SetCheckinTicket("")` 清除。
- SDK 不自动重试或跟随重定向，也不代替调用方登录。票据的有效期未验证，SDK 不推断或缓存 TTL。
- 没有票据时，SDK 在联网前返回 `*CheckinTicketError`；服务端返回 `46001`、`46002`、`46003` 时也映射为该类型。具体业务码细分含义未验证。
- 服务端拒绝操作票据时，前端要求回到「成电智慧学工」小程序重新授权。SDK 不伪造或绕过票据，也不会自动重试打卡、填报或班委上报。
- 没有向线上提交打卡或填报；写入协议和票据获取流程由本地 `httptest` 服务验证。

### 权限范围

最终权限由服务端判断。业务码 `4003` 映射为 `*AuthorizationError`，HTTP 403 也映射为授权错误；票据业务码优先于 HTTP 403。班委接口要求 `userData.userType == "CLASS_CADRE"`，接口契约是前端推导，未用普通学生账号探测其他班级。

前端还检查签到时段、宿舍签到点、距离、当前记录状态和情况关键字备注。要求备注的关键字，前端要求去除两端空白后至少 10 字；SDK 不代替服务端判定业务规则，也不伪造定位、设备信息或时间。

## 接口清单

| Go 方法 | HTTP 与路径 | 返回 `Data` |
| --- | --- | --- |
| `Auth.UserInfo(ctx, sessionID)` | GET `/api/auth/user-info` | `UserInfo` |
| `Auth.RequestCheckinTicket(ctx, code)` | POST origin + `/platform-app/api/auth/checkin-ticket` | `CheckinTicket`；需原始 `ok: true` |
| `Student.EffectiveSchedule(ctx)` | GET `/api/checkin-schedule/effective` | `*CheckinSchedule` |
| `Student.AvailableLocations(ctx)` | GET `/api/checkin-location/available-locations` | `[]CheckinLocation` |
| `Student.CampusFences(ctx)` | GET `/api/campus-fence/current-student` | `[]CampusFence` |
| `Student.Today(ctx)` | GET `/api/student-checkin-record/today` | `*CheckinRecord` |
| `Student.History(ctx, query)` | GET `/api/student-checkin-record/list` | `[]CheckinRecord`，另有 `Page` |
| `Student.Checkin(ctx, request)` | POST `/api/student-checkin-record/checkin` | `json.RawMessage`；需票据 |
| `Student.Report(ctx, request)` | POST `/api/student-checkin-record/report` | `json.RawMessage`；需票据 |
| `Student.LogLocation(ctx, request)` | POST `/api/student-checkin-record/location-log` | `json.RawMessage` |
| `Student.CaseKeywords(ctx, type)` | GET `/api/case-keyword/active` | `[]CaseKeyword` |
| `Student.ReverseGeocode(ctx, lat, lng)` | GET `/api/map/reverse` | `*string` |
| `Student.SituationTypes(ctx)` | POST `/api/common/attachMore` | `*SituationTypes`；只读字典 |
| `Student.DownloadAvatar(ctx, path)` | GET `https://jzsz.uestc.edu.cn/fastdfs-util/api/fastdfs/download/image` | `[]byte` |
| `ClassCadre.MyClasses(ctx)` | GET `/api/class-cadre/my-classes` | `[]ManagedClass` |
| `ClassCadre.CheckinList(ctx, query)` | GET `/api/class-cadre/checkin-list` | `[]CheckinRecord`，另有 `Page` |
| `ClassCadre.CheckinCount(ctx, date)` | GET `/api/class-cadre/checkin-count` | `*Int64` |
| `ClassCadre.ReportSituations(ctx, reports)` | POST `/api/class-cadre/situation/report` | `json.RawMessage`；上报体是数组 |

## 类型与边界

- `Date` 是 `YYYY-MM-DD` 的 civil date；`DateTime` 是服务端本地墙钟时间，按 `YYYY-MM-DD HH:mm:ss` 发送。`DateTimeNow()` 使用固定 UTC+8；`DateTimeFromTime` 只保留显示的年月日时分秒，不发送时区偏移。响应时间戳不带时区信息。
- `Int64` 同时接受 JSON 数字和数字字符串。分页 `totalCount` 在生产中可能是字符串；不经过浮点数，保留完整精度。`PageInfo.TotalCount`/`TotalPage` 是 `Int64`。
- `InstructorList` 接受 JSON 字符串、真实数组、空字符串或 null，输出 `[]InstructorContact`。旧单联系人字段仍保留，不合成额外联系人。
- 可空字段使用指针；JSON null 变成 nil。标量端点用 `*bool`、`*string`、`*Int64`，字典端点用 `*SituationTypes`，以区分缺失/null 与合法零值；缺失时方法返回 `*ProtocolError`。列表端点的 `null`/缺失 `data` 变成 nil slice，不额外报错，这与 Go 的空集合习惯一致。未知响应字段会被 `encoding/json` 静默丢弃；Python SDK 会保留在 `model_extra`，这是两者的差异。
- 无法从样本确定结构的免打卡详情、学生标签、异常追踪和写操作结果保留为 `json.RawMessage`，没有伪造字段。
- 本地校验：经纬度范围与 NaN/Inf、非负精度与耗时、年月范围、页大小、位置类型、坐标系和关键记录必填字段。校验失败返回 `*ValidationError`，契约不匹配返回 `*ProtocolError`，且发生在返回半成品数据之前。
- 请求使用 `context.Context`；取消和超时会原样返回 `context`/网络错误。SDK 不重试任何请求。
- 请求体字段使用服务端 camelCase；Go 源码字段使用 PascalCase。没有反射式映射，字段名编译期固定。

## 错误处理

```go
import (
	"errors"
	"fmt"

	moonuestc "moon-uestc/go-sdk"
)

_, err := client.Student.Today(ctx)
switch {
case err == nil:
	// 成功
default:
	var authErr *moonuestc.AuthenticationError
	var ticketErr *moonuestc.CheckinTicketError
	var apiErr *moonuestc.APIError
	var protocolErr *moonuestc.ProtocolError
	switch {
	case errors.As(err, &authErr):
		fmt.Println("登录态失效，请从官方入口重新登录")
	case errors.As(err, &ticketErr):
		fmt.Println("打卡授权失效，请从小程序重新进入")
	case errors.As(err, &apiErr):
		fmt.Println("业务失败", apiErr.Code, apiErr.Message)
	case errors.As(err, &protocolErr):
		fmt.Println("响应不符合契约")
	default:
		fmt.Println(err)
	}
}
```

`CheckinTicketError` 会依次解包到 `AuthorizationError` 和 `APIError`，因此 `errors.As` 可以按需要的层级判断。HTTP 401/403、其它非 2xx、重定向、登录 HTML 和响应类型错误分别有对应类型；错误消息不包含 Cookie、票据、登录 code 或完整响应正文。

## 验证

```sh
cd go-sdk
gofmt -l .
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go test -cover ./... -count=1

# 可选：真实服务匿名认证检查
MOON_LIVE=1 go test -run TestLiveAnonymousSessionRejected -v
```

截至新增显式票据获取 API 之前的记录：本地 24 个顶层测试通过，语句覆盖率 83.5%，覆盖当时已有的 18 个路径/方法及认证、Cookie、重定向、响应契约和请求校验；`go test -race` 通过。真实服务匿名请求返回 `5000` 并正确映射为 `AuthenticationError`。这些数字不包含本次新增的票据获取测试。

没有用真实 SESSION 重新执行 Go 端已认证读取；该部分沿用 Python SDK 的线上只读验证。没有向线上提交任何打卡、填报、定位日志或班委上报。真实打卡仍需要官方小程序的有效票据。

本次新增取票 API 后已运行 `gofmt`、`go vet ./...` 和 `go test -race ./... -count=1 -v`。另以独立的本地 HTTP 场景执行真实 SDK 调用，验证只含 `ok: true` 的取票成功响应、SESSION、原始 code 请求体、显式安装前禁止提交、安装后的打卡/填报请求头、失败不替换旧票据、自定义业务前缀及普通 H5 读取不受影响。没有调用线上签发接口，也没有取得或验证真实微信 code。
