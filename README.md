# moon-uestc

「晚安成电」学生端 Python 与 Go SDK。Python 3.13+，同步 `httpx2` 客户端，Pydantic 请求/响应模型，包含 `py.typed`；Go 1.23+，只用标准库，见 [`go-sdk/`](go-sdk/README.md)。两者都只覆盖学生端及学生身份下的班委功能，不含辅导员、学院、校级管理接口。

契约来自 Chrome 中部署的 H5 前端及本人账号的只读响应，不是官方公开 API。逆向日期：2026-10-22。前端未引用的后台接口不在覆盖范围内。

## Python SDK 安装与只读使用

在项目目录执行：

```sh
uv sync
```

只将自己的 `SESSION` Cookie 放入环境变量 `MOON_SESSION`，不要提交到代码仓库。可在已登录页面的 Chrome DevTools → Application → Cookies → `https://jzsz.uestc.edu.cn` 中查看。它是 HttpOnly Cookie，`document.cookie` 不能读取。

```python
import os
from datetime import datetime
from zoneinfo import ZoneInfo

from moon_uestc import MoonClient
from moon_uestc.models import HistoryQuery, LocationType

with MoonClient(session=os.environ["MOON_SESSION"]) as client:
    user = client.auth.user_info().data.user_data
    print(user.full_name, user.role)

    record = client.student.today().data
    if record is not None:
        print(record.business_date, record.record_status_code)
        for contact in record.instructor_list:
            print(contact.name)  # 服务端的 JSON 字符串已解析成联系人模型

    month = datetime.now(ZoneInfo("Asia/Shanghai"))
    response = client.student.history(
        HistoryQuery(year=month.year, month=month.month, page_index=1, page_size=10)
    )
    for item in response.data:
        print(item.business_date, item.record_status_code)
    if response.page is not None:
        print(response.page.total_count, response.page.total_page)

    keywords = client.student.case_keywords(LocationType.CAMPUS).data
```

所有 JSON 接口返回 `APIResponse[T]`，业务失败直接抛异常；成功后 `.data` 是已校验的领域类型，分页元信息保留在 `.page`。`download_avatar()` 返回原始 `bytes`。客户端持有连接池，使用 `with` 或显式 `close()` 关闭。

## Go SDK

`go-sdk/` 是独立 Go module，Go 1.23+，只使用标准库。方法、路径、SESSION、操作票据和错误映射与 Python 版本一致。当前 module 路径为 `moon-uestc/go-sdk`，发布前应改成实际仓库地址；详见 [go-sdk/README.md](go-sdk/README.md)。

```go
package main

import (
    "context"
    "fmt"
    "os"

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

    record, err := client.Student.Today(context.Background())
    if err != nil {
        panic(err)
    }
    if record.Data != nil {
        fmt.Println(record.Data.BusinessDate, record.Data.RecordStatusCode)
    }
}
```

Go 版本使用 `context.Context`，通过 `errors.As` 区分 `AuthenticationError`、`AuthorizationError`、`CheckinTicketError` 和 `ProtocolError`。`Date`、`DateTime`、`Int64` 和联系人列表都按真实响应边界处理；写入同样不会自动重试。

## 认证与授权

### 登录身份：SESSION

当前部署已实测使用服务端 `SESSION` Cookie。只带该 Cookie 的 Python 请求可以正常读取学生数据，不依赖浏览器 UA、地图 API Key 或 `appId=294`。SDK 按前端惯例发送 `platform: h5`，不自动跟随重定向。

- 无 Cookie 的 `auth/user-info` 返回 HTTP 200、业务码 `5000`，提示“未登录或会话已失效”，SDK 将其转换为 `AuthenticationError`。
- 实测登录响应的 `accessToken` 和 `refreshToken` 都是 `NOT_IN_USE`，不是可用的 Bearer 凭据。
- 前端另有 `userData.token` → `Authorization: Bearer ...` 分支。SDK 提供可选 `bearer_token` 参数，但当前会话没有该 token，未验证 Bearer 单独认证。
- 前端入口可读取 URL 查询参数 `sessionId`，传给 `auth/user-info`。对应 `client.auth.user_info(session_id=...)`；未取得有效入口样本，未验证其换取会话的行为。
- 学生前端未发现密码登录、刷新 token 或延长会话的接口。失效后应从官方入口重新登录，再更新 Cookie；SDK 不自动续期。
- 当前观察到 `SESSION` 的 HttpOnly 为 true、Secure 为 false。始终使用 HTTPS，勿把会话传给第三方、日志或公开下载链接。

### 打卡与情况填报：独立操作票据

`checkin`、`report` 除登录身份外还使用 `X-Checkin-Ticket`。前端从官方小程序进入 H5 时的哈希查询参数 `checkinTicket` 接收票据，将其存入 `sessionStorage["daily-checkin-operation-ticket"]`，随后从地址栏移除该参数。

- 登录 Cookie 不等于操作授权。当前浏览器没有该票据，因此未实测线上打卡或填报。
- 使用官方入口获得的有效票据创建 `MoonClient(session=..., checkin_ticket=...)`；重新授权后可调用 `set_checkin_ticket(...)`，传 `None` 清除。
- 没有票据时，SDK 在联网前抛 `CheckinTicketError`。服务端返回 `46001`、`46002`、`46003` 时也映射为该异常；具体码的细分含义未验证。
- 前端针对这些码会清除旧票据，并要求回到「成电智慧学工」小程序重新授权。SDK 不伪造、获取或绕过票据，不自动重试业务写入。

### 角色和业务约束

本人账号的当前角色为学生 `001`。前端仅在 `userData.userType == "CLASS_CADRE"` 时展示班委页面；班委操作单独放在 `client.class_cadre` 下。SDK 不靠客户端字段授予权限，最终权限和数据范围由服务端判断。业务码 `4003` 映射为 `AuthorizationError`。

前端还检查签到时段、宿舍签到点、距离、当前记录状态、情况关键字及备注要求。围栏和坐标使用 GCJ-02，校内/校外类型为 `CAMPUS` / `OUTSIDE`。这些是前端可见约束，不代表已完整验证服务端校验策略。SDK 不把任意坐标、设备信息或时间伪装成真实定位。

## 学生接口清单

除头像外，以下路径都相对于 `https://jzsz.uestc.edu.cn/daily-checkin`。表中的“实测”仅指真实只读调用，不包括业务写操作。

| SDK 方法 | HTTP 与路径 | 参数或请求体 | 返回 `.data` | 验证范围 |
| --- | --- | --- | --- | --- |
| `auth.user_info()` | GET `/api/auth/user-info` | 可选 `sessionId` | `UserInfo` | SESSION 与匿名行为已实测；sessionId 分支未测 |
| `auth.service_account_subscribed()` | GET `/api/user/service-account-subscribed` | 无 | `bool` | 已实测 |
| `student.effective_schedule()` | GET `/api/checkin-schedule/effective` | 无 | `CheckinSchedule \| None` | 已实测 |
| `student.available_locations()` | GET `/api/checkin-location/available-locations` | 无 | `list[CheckinLocation]` | 已实测 |
| `student.campus_fences()` | GET `/api/campus-fence/current-student` | 无 | `list[CampusFence]` | 已实测 |
| `student.today()` | GET `/api/student-checkin-record/today` | 无 | `CheckinRecord \| None` | 非空记录已实测；空值有回归测试 |
| `student.history(query)` | GET `/api/student-checkin-record/list` | `HistoryQuery` | `list[CheckinRecord]`，另有 `.page` | 已实测 |
| `student.checkin(request)` | POST `/api/student-checkin-record/checkin` | `CheckinRequest`，操作票据 | `JsonValue` | 前端契约；未向线上提交 |
| `student.report(request)` | POST `/api/student-checkin-record/report` | `ReportRequest`，操作票据 | `JsonValue` | 前端契约；未向线上提交 |
| `student.log_location(request)` | POST `/api/student-checkin-record/location-log` | `LocationLogRequest` | `JsonValue` | 前端契约；未向线上提交 |
| `student.case_keywords(location_type)` | GET `/api/case-keyword/active` | `locationTypeCode` | `list[CaseKeyword]` | 校内、校外均已实测 |
| `student.reverse_geocode(latitude=..., longitude=...)` | GET `/api/map/reverse` | `lat`、`lng` | `str` | 已实测 |
| `student.situation_types()` | POST `/api/common/attachMore` | `[{"target":"SITUATION_TYPE"}]` | `SituationTypes` | 只读字典已实测 |
| `student.download_avatar(file_path)` | GET `https://jzsz.uestc.edu.cn/fastdfs-util/api/fastdfs/download/image` | `filePath`，取自 `life_head_url` | 原始 `bytes` | 已实测；服务端可能不带 Content-Type |
| `class_cadre.my_classes()` | GET `/api/class-cadre/my-classes` | 无 | `list[ManagedClass]` | 前端契约；无班委账号，未实测 |
| `class_cadre.checkin_list(query)` | GET `/api/class-cadre/checkin-list` | `ClassCheckinQuery` | `list[CheckinRecord]`，另有 `.page` | 前端契约，未实测 |
| `class_cadre.checkin_count(business_date)` | GET `/api/class-cadre/checkin-count` | `businessDate` | `int` | 前端按数字使用，未实测 |
| `class_cadre.report_situations(reports)` | POST `/api/class-cadre/situation/report` | `list[ClassSituationReport]` | `JsonValue` | 前端契约；未向线上提交 |

### 请求模型

从 `moon_uestc.models` 导入。构造函数使用 snake_case；发给服务端时按契约转为 camelCase。请求拒绝未知字段，纬度限定在 `[-90, 90]`，经度限定在 `[-180, 180]`，不接受 NaN/Infinity。

- `HistoryQuery`：必填 `year: int`、`month: int`；`page_index=1`、`page_size=10`；`clock_in=True/False/None` 分别对应已打卡、未打卡、全部。服务端参数为 `year`、`month`、`pageIndex`、`pageSize`、`clockIn`，最后一个序列化为 `"true"`、`"false"`、`""`。
- `CheckinRequest`：必填 `client_time: datetime`、`latitude`、`longitude`、`reverse_geocode`；可选 `user_agent`、`accuracy`、`location_duration_ms`、`network_type_code`、`location_enabled`、`location_authorized`、`location_accuracy`、`error_message`。`coordinate_system_code` 固定为 `"gcj02"`。这些诊断值由调用方提供，不自动编造。
- `ReportRequest`：必填 `reported_location_type_code: LocationType`、`case_keyword_id`、`latitude`、`longitude`、`reverse_geocode`；可选 `case_keyword`、`gps_accuracy_meter`、`case_remark`（默认空字符串）；坐标系固定为 `"gcj02"`。先读取情况关键字的 `remark_required`；前端对要求备注的关键字限制备注去除两端空白后至少 10 字，服务端仍有最终决定权。
- `LocationLogRequest`：定位失败或权限诊断，包含与 `CheckinRequest` 相同的诊断字段和必填 `client_time`，不含经纬度与逆地址字段。SDK 不自动上传遥测。
- `ClassCheckinQuery`：必填 `business_date: date`、`class_key: str`；可选 `keyword`、`page_index`、`page_size`。
- `ClassSituationReport`：必填 `class_key`、`record_id`、`location_type_code`、`case_keyword_id`；可选 `case_remark`。批量上报请求体是对象数组，不是包裹在 `records` 内的对象。

`client_time` 需为服务端本地墙钟时间的 naive `datetime`，按 `YYYY-MM-DD HH:mm:ss` 序列化，拒绝带时区值以免静默改变含义。接口响应日期解析为 `date`，时间戳解析为 naive `datetime`，SDK 不擅自添加时区。签到时段偏移是相对 `business_date` 零点的分钟数，可能超过 1440，不能简单对 24 小时取模。

### 类型边界

- `CheckinRecord.instructor_list` 同时接受服务端的 JSON 字符串、数组及 null，并返回 `list[InstructorContact]`。旧的单联系人字段 `instructor_sn/name/phone` 仍作为服务端原始字段保留，不合成额外联系人。
- ID 保留字符串；分页中的字符串计数转为 Python `int`，不经过浮点数。
- 响应保留新增字段，可通过 Pydantic 的 `model_extra` 访问。字段类型不匹配会抛 `ProtocolError`，而不是返回未校验的字典。
- 真实样本无法确定的免打卡详情、学生标签内部结构、异常追踪内容和写操作结果明确使用 `JsonValue`，不声称这些部分已经有完整结构定义。写操作若成功但省略 `data`，SDK 将其视为 `None`。
- 班委列表复用学生记录模型，班级选项来自前端实际读取的 `classKey`、`manageClass`。没有班委账号，不能保证当前服务端每个字段与推导完全一致；不会以普通学生会话探测其他班级。

## 错误处理

```python
import os

from moon_uestc import AuthenticationError, AuthorizationError, MoonClient

try:
    with MoonClient(session=os.environ["MOON_SESSION"]) as client:
        record = client.student.today().data
except AuthenticationError:
    print("请从官方入口重新登录并更新 SESSION")
except AuthorizationError as error:
    print("当前身份没有权限", error.code)
```

`CheckinTicketError` 是 `AuthorizationError` 的子类；其它业务错误为 `APIError`。操作票据业务码优先识别，包括 HTTP 403 中的票据错误；其余 HTTP 401/403 分别映射为认证/授权异常。其它非成功 HTTP 响应保留 `httpx2.HTTPStatusError`，网络错误保留 `httpx2` 原始异常。重定向、登录 HTML 和不匹配的响应契约为 `ProtocolError`。SDK 不重试，不吞掉错误，也不在错误消息中附带请求 Cookie、票据或完整响应正文。

## 逆向依据与验证

Chrome 获取的学生前端入口：

- [主 bundle](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/index-BsTuEIoc.js)：请求拦截、SESSION 入口、角色菜单、操作票据保存和错误处理。
- [学生 API 模块](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/student.KytIoD5T.js)：8 个本人接口、4 个班委接口。
- [学生今日页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-report-today-home-index.pcAPAkGF.js)：打卡、填报、定位诊断请求体及逆地址接口。
- [历史记录页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-historical-check-in-home-index.BHcOibFM.js)：年月筛选和 clockIn 三态。
- [班委页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-class-check-in-home-index.HSDyIZfu.js)：班级查询、计数、批量情况上报。
- [公共模块](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/index.DNnsTcL3.js)、[头像组件](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/Avatar.D0sun_0x.js)：情况关键字、附加字典、FastDFS 图片地址。

已用 Python SDK 对真实服务执行表中标记的只读路径，并确认匿名请求失败。没有打卡、填报、定位日志或班委业务写入；没有测试管理权限或他人数据。凭据和真实个人响应未写入仓库。

本地检查：

```sh
uv run pyright
uv run pyright --verifytypes moon_uestc --ignoreexternal
uv run python -m unittest discover -s tests -v
```

严格类型检查通过，发布接口的类型完整性检查为 100%；14 个回归测试通过，覆盖认证/授权业务码、票据前置条件、非 2xx 票据错误、不重试提交、重定向不泄露凭据、空记录与坏记录、JSON 内嵌联系人、大整数分页、三态历史筛选、坐标边界及本地时间格式。另用本地 HTTP 服务运行了打卡、填报、诊断日志协议，验证 Cookie、票据、序列化、无 data 成功响应及 HTTP 403 错误映射。`uv build` 可生成 wheel 和源码包，wheel 包含类型标记。真实打卡授权仍需官方小程序有效票据；这里的写操作验证不代表线上提交成功。

Go SDK 另通过 `gofmt`、`go vet`、24 个本地测试和 `go test -race`，并针对真实服务验证了匿名 SESSION 缺失会返回 `AuthenticationError(code=5000)`。Go 客户端没有用真实 SESSION 重跑已认证读取，也没有线上提交写入；这部分沿用 Python 的线上只读结果和本地 HTTP 写入协议验证。Go 测试命令见 [go-sdk/README.md](go-sdk/README.md)。
