# moon-uestc

「晚安成电」学生端 Python 与 Go SDK。Python 3.13+，同步 `httpx2` 客户端，Pydantic 请求/响应模型，包含 `py.typed`；Go 1.23+，只用标准库，见 [`go-sdk/`](go-sdk/README.md)。业务接口只覆盖学生端及学生身份下的班委功能，不含管理接口。Python 另提供独立的统一身份认证 `IDASClient`。

契约来自部署的 H5 前端、小程序静态代码及本人账号的只读响应，不是官方公开 API。逆向日期：2026-10-22～23。业务覆盖范围见下表，不含管理接口。

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

普通 JSON 接口返回 `APIResponse[T]`，业务失败直接抛异常；成功后 `.data` 是已校验的领域类型，分页元信息保留在 `.page`。`download_avatar()` 返回原始 `bytes`；复合认证方法 `auth.exchange_session()` 直接返回 `CASSession`。客户端持有连接池，使用 `with` 或显式 `close()` 关闭。

## Go SDK

`go-sdk/` 是独立 Go module，Go 1.23+，只使用标准库。学生业务方法、路径、SESSION、操作票据和错误映射与 Python 版本一致；显式 CAS 换 SESSION 方法目前仅 Python 支持。当前 module 路径为 `moon-uestc/go-sdk`，发布前应改成实际仓库地址；详见 [go-sdk/README.md](go-sdk/README.md)。

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

当前部署已实测使用服务端 `SESSION` Cookie。只带该 Cookie 的 Python 请求可以正常读取学生数据，不依赖浏览器 UA、地图 API Key 或 `appId=294`。SDK 按前端惯例发送 `platform: h5`，普通业务请求不自动跟随重定向。

- 无 Cookie 的 `auth/user-info` 返回 HTTP 200、业务码 `5000`，提示“未登录或会话已失效”，SDK 将其转换为 `AuthenticationError`。
- 实测登录响应的 `accessToken` 和 `refreshToken` 都是 `NOT_IN_USE`，不是可用的 Bearer 凭据。
- 前端另有 `userData.token` → `Authorization: Bearer ...` 分支。SDK 提供可选 `bearer_token` 参数，但当前会话没有该 token，未验证 Bearer 单独认证。
- 前端入口可读取 URL 查询参数 `sessionId`，传给 `auth/user-info`。对应 `client.auth.user_info(session_id=...)`；未取得有效入口样本，未验证其换取会话的行为。
- 学生前端未发现密码登录或刷新 token 的接口。Python 的独立 `IDASClient` 支持统一认证账号密码登录，再通过 `auth.exchange_session()` 换 SESSION；普通业务客户端不会自动登录、续期或代替官方二次认证。
- 当前观察到 `SESSION` 的 HttpOnly 为 true、Secure 为 false。始终使用 HTTPS，勿把会话传给第三方、日志或公开下载链接。

### 统一身份认证账号密码登录（Python）

`IDASClient` 使用独立连接池和 Cookie jar，不接收打卡 SESSION、Bearer 或操作票据。基础用法：

```python
from getpass import getpass

from moon_uestc import IDASClient, MoonClient, login_interactive

with IDASClient(user_agent="moon-uestc/0.1") as idas:
    browser_fingerprint = idas.register_fingerprint()
    credentials = login_interactive(
        idas,
        input("学号/工号：").strip(),
        getpass("统一认证密码："),
        browser_fingerprint=browser_fingerprint,
        remember_me=True,
    )

# 直接兼容现有 CAS 换会话方法；不会更改已有 MoonClient 的身份。
with MoonClient() as bootstrap:
    exchanged = bootstrap.auth.exchange_session(
        castgc=credentials.castgc,
        browser_fingerprint=credentials.browser_fingerprint,
        user_agent=credentials.user_agent,
    )
with MoonClient(session=exchanged.session) as client:
    record = client.student.today().data
```

`login()` 获取当前登录页的 `execution` 和加密盐，保留 JSESSIONID，检查验证码，再使用站点相同的 AES-CBC/PKCS7 加密提交密码。64 字符随机前缀和 16 字符随机 IV 均使用安全随机源。请求不携带明文 `passwordText`；密码错误不会自动重试。`remember_me=True` 只请求站点的免登录选项，不保证有效期。

成功只返回服务端实际下发的 `CASCredentials`。`castgc` 和 `browser_fingerprint` 是 `SecretStr`，常规 repr/JSON 会遮蔽；需要持久化时才调用 `get_secret_value()`，并由应用自行安全保存。`close()` 只关闭本地客户端；`logout()` 才会请求服务端注销并清空本地状态。

#### 指纹注册

`register_fingerprint(value=None) -> SecretStr` 调用 `GET /authserver/bfp/info?bfp=...`，并确认服务端确实保留了同值 Cookie，不能仅凭 HTTP 200 判断成功。该接口不要求已登录；指纹 Cookie 不是通过 MFA 的证明。

- 显式传值时，要求 32 位大写十六进制；可传已有浏览器的 localStorage `browser_fingerprint` 或同名 Cookie 值。
- 上面的示例调用 `register_fingerprint()` 在 Python 中生成并注册设备标识，不需要设置 `MOON_BROWSER_FINGERPRINT`。同一客户端会复用返回的 Cookie；不传 `browser_fingerprint` 的 `login()` 也会按需生成。
- SDK 设备标识不是伪造的 Chrome Canvas 指纹。首次登录或更换标识可能触发二次认证；跨进程需要复用时，由应用安全保存返回值。
- 要复用浏览器已有的设备信任，应同时使用对应的实际 User-Agent 和指纹，而不是示例中的 SDK User-Agent。
- `FingerprintComponents(...).digest()` 可根据调用方采集的 UAParser 字段、navigator 属性及 Canvas PNG 字节，复现站点的双层大写 MD5；不会猜测浏览器属性或在 Python 中伪装 Canvas 渲染。

`uv run python main.py` 会在脚本目录的 `.moon-uestc-auth.json` 中缓存 SESSION、CASTGC、设备指纹和 User-Agent，权限为 `0600`，不保存密码。再次运行时直接用 SESSION 查询今日记录；服务端拒绝会话后，先用缓存的 CAS 凭据换取新 SESSION，保存服务端轮换后的凭据。只有缺少 CAS 凭据或明确要求重新认证时才提示输入账号密码；网络故障和权限不足不会触发重新登录。指纹在交互登录前保存，取消登录后仍可复用。缓存使用临时文件原子替换，已加入 `.gitignore`，但内容仍是明文登录凭据，不要分享或提交。需要切换账号时删除该文件；这只清除本地缓存，不注销服务端会话。以上逻辑仅在 `main.py`，SDK 的认证行为不变。

遇到 `/authserver/reAuthCheck/reAuthLoginView.do` 时，`main.py` 会读取官方页面提供的认证方式；提供短信方式时，在同一 Cookie 会话内切换认证、等待用户确认发信，再读取隐藏输入的短信验证码。可选择“信任此设备”（默认不信任）；短信和密码均不写入缓存。验证后继续原 CAS 跳转，只有平台登录检查和身份校验都通过才保存 SESSION。验证码被拒绝时不会改为重复密码登录；没有短信方式时需在官方页面完成认证。此流程使用 SDK 的自定义 transport 接口，不修改 SDK。

#### 验证码与二次认证

需要验证码时，`login()` 在密码 POST 前抛 `CaptchaRequiredError`，`.kind` 为 `"image"` 或 `"slider"`。保留同一 `IDASClient` 完成验证，再调用 `login()`；不要重新 `prepare_login()`、换客户端或丢弃 JSESSIONID。

也可以直接运行登录命令：

```sh
uv run moon-uestc login --username 你的学号 --output "$HOME/.moon-uestc-cas.json"
# 等价入口
uv run python -m moon_uestc login --output "$HOME/.moon-uestc-cas-new.json"
```

用户名未传时会提示输入，密码始终用隐藏输入读取，不提供 `--password` 参数；终端无法关闭密码回显时会中止。可选 `--remember-me`、`--user-agent`、`--browser-fingerprint` 与 SDK 同义。

验证码直接用 ANSI 真彩色半块字符显示，不打开浏览器或外部图片查看器；需要支持 Unicode 和真彩色的交互终端，不能将解题输入输出重定向到文件。

| 题型 | 人工操作 |
| --- | --- |
| 图片 | 在 `Answer:` 输入字符，Enter 提交；Ctrl-R / F5 换题；Ctrl-C / Esc 取消 |
| 滑块 | 左右方向键或 h/l 每次移动 1 像素；Shift+方向键或 H/L 每次移动 5 像素；Enter 校验；r 换题；q / Ctrl-C / Esc 取消 |

滑块只记录用户实际按键对应的位移和采样间隔，不自动找缺口、补点或制造随机轨迹；上下方向键及 j/k 记录用户的竖向输入，拼图本身仍只横向移动。未移动就按 Enter 不会提交。只有服务端确认通过才继续登录；拒绝会保留交互界面，可手动换题或取消。键盘交互轨迹是否被真实服务接受仍需用户登录验证，不把本地成功当成线上通过。

登录成功后，`--output` 指定的新 JSON 文件保存真实 `castgc`、`browser_fingerprint`、`user_agent`，权限为 `0600`；标准输出仅打印路径。文件含登录凭据，不要提交到仓库。已有文件或符号链接会被拒绝，不覆盖旧凭据。命令只登录统一认证，不执行打卡或其他业务写入。

Python 中使用导出的 `login_interactive(idas, username, password, ...)` 可复用同一套终端交互。它在原客户端内完成一次验证码续交，保留 JSESSIONID，不自动重试被拒绝的密码。

滑块交互分两步，SDK 不自动识别拼图、不生成鼠标轨迹：

1. `get_slider_captcha() -> SliderCaptcha` 返回 `background_image`、`puzzle_image` 字节，交给应用自己的交互界面展示。拼图字节末尾携带签名密钥，SDK 自动提取。
2. `verify_slider_captcha(canvas_length=..., move_length=..., tracks=...) -> bool` 提交实际交互结果。`tracks` 为 `SliderTrack(a=横向位移, b=纵向位移, c=距前一采样点的毫秒数)` 序列；画布宽度、总移动距离都使用像素。SDK 按站点算法加密签名，只有服务端 `errorCode=1` 才视为通过。通过后在同一客户端再次 `login()`；失败则重新取题交互。

登录失败响应中的新 execution 会替换旧状态；一次密码提交会消耗本地滑块验证状态。`prepare_login()` 主动开启新表单流程，也会清除本地旧 CASTGC 和验证码状态，不会在后台注销旧票据。

SDK 的密码登录或换会话流程停在二次认证页时抛 `ReauthenticationRequiredError`，不把指纹 Cookie、HTTP 200、旧 CASTGC 或验证码通过误当成登录成功。SDK 本身不提供短信/TOTP/FIDO 二次认证提交；`main.py` 单独处理换会话阶段官方页面提供的短信认证，其他方式仍需在官方流程完成后再换会话。

| 方法 | 行为 |
| --- | --- |
| `prepare_login()` | 读取并保留当前登录表单，返回 `IDASLoginPage` |
| `needs_captcha(username)` | 查询当前账号是否需要验证码，严格解析布尔结果 |
| `register_fingerprint(value=None)` | 注册或复用服务端指纹 Cookie |
| `get_image_captcha()` | 获取图片验证码字节 |
| `get_slider_captcha()` | 获取滑块题目图片 |
| `verify_slider_captcha(...)` | 提交调用方采集的滑块测量和轨迹 |
| `login(username, password, ...)` | 密码登录并返回新的 `CASCredentials` |
| `credentials()` | 读取当前保留的、作用域正确的认证 Cookie；不验证服务端是否仍接受它 |
| `logout()` | 显式请求注销，成功后清空本地 Cookie 和表单状态 |

所有请求固定发往 HTTPS `idas.uestc.edu.cn`。密码登录的跳转最多 8 次，仅限相同认证 origin；不接受外域、用户凭据 URL、额外端口或 HTTP 降级。Cookie 名冲突或作用域不符为 `ProtocolError`，网络及非成功 HTTP 响应为已去除敏感请求详情的 `AuthenticationError`。`transport` 可注入独立 HTTP transport，用于测试或自定义连接方式。

### 显式换取新的 SESSION（Python）

`client.auth.exchange_session(castgc=..., browser_fingerprint=..., user_agent=...) -> CASSession` 使用你自己仍有效的统一认证会话换取新 SESSION，不要求已有打卡 SESSION。

两项 Cookie 都来自 `idas.uestc.edu.cn`：`CASTGC` 的路径为 `/authserver`，`MULTIFACTOR_BROWSER_FINGERPRINT` 的路径为 `/`。`user_agent` 使用对应登录浏览器的实际值。仅有 Cookie 不代表它仍有效；实测仅带 CASTGC，即使 UA 相同，也可能进入二次认证页。

```python
import os

from moon_uestc import MoonClient, ReauthenticationRequiredError

try:
    with MoonClient() as bootstrap:
        exchanged = bootstrap.auth.exchange_session(
            castgc=os.environ["MOON_CASTGC"],
            browser_fingerprint=os.environ["MOON_BROWSER_FINGERPRINT"],
            user_agent=os.environ["MOON_USER_AGENT"],
        )
except ReauthenticationRequiredError:
    raise SystemExit("请先在官方页面完成登录或二次认证")

# session 是 SecretStr，MoonClient 可直接接收，无需转成明文。
with MoonClient(session=exchanged.session) as client:
    record = client.student.today().data

# 后续再次换取时，使用服务端可能已轮换的新凭据，而非一直复用原值：
next_credentials = exchanged.credentials
```

返回类型从 `moon_uestc.models` 导入：

- `CASSession.session: SecretStr`：新 SESSION。
- `CASSession.user_info: UserInfo`：使用新会话读取并校验后的身份，不是当前客户端原身份的副本。
- `CASSession.credentials: CASCredentials`：响应 Cookie 更新后的 `castgc`、`browser_fingerprint`（均为 SecretStr），以及 `user_agent`。再次调用时传入这些值；SDK 不自动持久化凭据。常规 repr 和 JSON 序列化会遮蔽 SecretStr，确实需要明文时才显式调用 `get_secret_value()`。

方法使用独立连接池和 Cookie jar，顺序为 CAS `/authserver/login?service=https%3A%2F%2Fjzsz.uestc.edu.cn%2F` → Ticket 回调下发 SESSION → `/api/isLoginFailure` 确认 `status=true, code=200, data=true` → 打卡 `/api/auth/user-info` 验证身份。新 Cookie 直接访问打卡接口曾返回 5000，因此保留平台登录检查步骤。

只有换会话流程允许最多 8 次重定向，目标限于 HTTPS 的 `idas.uestc.edu.cn` 和客户端配置的业务 origin；拒绝 URL 用户凭据、额外域名和认证域 HTTP 降级。普通学生 API 仍不跟随重定向。当前客户端的 SESSION、Bearer 和打卡票据不会发送给 CAS；SSO Cookie 不发送给业务域。高级调用方可传独立的 `transport` 参数控制本次换会话的 HTTP transport，它不复用业务客户端的 transport。

成功与失败都不修改当前 `MoonClient`：调用方自行用返回的 SESSION 创建新客户端；打卡票据不会自动迁移。停在官方登录或二次认证页时抛 `ReauthenticationRequiredError`（`AuthenticationError` 子类）；平台未确认登录也抛该异常。换会话的 CAS/平台登录请求发生网络或 HTTP 失败时抛 `AuthenticationError`，不在消息中暴露带 Ticket 的跳转 URL。返回结构不符或非法跳转为 `ProtocolError`。本方法不是延长旧 SESSION，也不保证会话或 SSO Cookie 的剩余寿命。


### 打卡与情况填报：独立操作票据

`checkin`、`report` 除登录身份外还使用 `X-Checkin-Ticket`。票据由后端签发，不是客户端本地计算的签名。小程序将票据通过 URL 的普通 query 或 hash query 参数 `checkinTicket` 交给 H5，H5 存入 `sessionStorage["daily-checkin-operation-ticket"]` 后从地址栏移除。

Python 使用 `auth.request_checkin_ticket(code=...)` 显式获取票据：

```python
import os

from moon_uestc import MoonClient

# 调用方在官方小程序环境执行 uni.login({provider: "weixin"})，取得真实 code。
# SDK 不执行微信登录；请勿使用 IDAS 登录凭据或 SESSION 代替该 code。
with MoonClient(session=os.environ["MOON_SESSION"]) as client:
    response = client.auth.request_checkin_ticket(code=os.environ["MOON_WECHAT_CODE"])
    client.set_checkin_ticket(response.data.ticket)
    # 此后 client.student.checkin(...) / report(...) 才携带返回的票据。
```

- 请求为同源 `POST /platform-app/api/auth/checkin-ticket`，JSON `{"code": code}`，请求头 `platform: mp-weixin`；复用当前连接池、超时及 SESSION Cookie。自定义 `base_url` 只影响取票的 origin，不将 `/daily-checkin` 或自定义业务前缀拼进取票路径。普通业务请求仍使用原 `base_url` 和 `platform: h5`。
- 原始响应必须为 `ok: true`，不能只凭 H5 的 `code: 0` 或 `status: true` 判定成功。`data.ticket` 必须恰好为 43 位 `[A-Za-z0-9_-]` 字符，否则抛 `ProtocolError`；这不证明其生成算法或有效期。
- 返回 `APIResponse[CheckinTicket]`，其中 `.data.ticket` 为 `SecretStr`，常规 repr/JSON 遮蔽。获取成功或失败都不会自动安装或替换当前票据；需要时显式调用 `set_checkin_ticket(...)`，传 `None` 清除。已有有效票据也可直接传给 `MoonClient(checkin_ticket=...)`。
- 登录 Cookie 不等于操作授权；SDK 无法仅凭 SESSION、账号密码或 CAS 凭据生成微信 code。没有票据时，提交在联网前抛 `CheckinTicketError`。服务端返回 `46001`、`46002`、`46003` 时也映射为该异常；各码的精确含义未验证。
- 前端遇到票据错误会清票并要求回到小程序重新授权。SDK 不伪造票据，不自动重新登录、续票或重试取票/业务写入。线上签发、打卡和填报未实测。

微信 code 的获取尚未集成。离线包中的 `uni.login` 依赖微信运行时，仅有解包 JavaScript 和 AppID 不能执行真实登录。官方 [`wx.login`](https://developers.weixin.qq.com/miniprogram/dev/api/open-api/login/wx.login.html) 在对应小程序环境返回 code；如具备目标 AppID 的开发权限和可运行工程，可通过官方 [`miniprogram-automator.callWxMethod`](https://developers.weixin.qq.com/miniprogram/dev/devtools/auto/miniprogram.html#miniProgram-callWxMethod) 调用。当前缺少该运行环境，SDK 没有用回调占位、其他 AppID 的 code 或本地随机字符串替代真实获取。

### 角色和业务约束

本人账号的当前角色为学生 `001`。前端仅在 `userData.userType == "CLASS_CADRE"` 时展示班委页面；班委操作单独放在 `client.class_cadre` 下。SDK 不靠客户端字段授予权限，最终权限和数据范围由服务端判断。业务码 `4003` 映射为 `AuthorizationError`。

前端还检查签到时段、宿舍签到点、距离、当前记录状态、情况关键字及备注要求。围栏和坐标使用 GCJ-02，校内/校外类型为 `CAMPUS` / `OUTSIDE`。这些是前端可见约束，不代表已完整验证服务端校验策略。SDK 不把任意坐标、设备信息或时间伪装成真实定位。

## 学生接口清单

除头像和取票外，以下路径都相对于 `https://jzsz.uestc.edu.cn/daily-checkin`；取票路径相对于同源 origin。表中的“实测”仅指真实只读调用，不包括业务写操作。

| SDK 方法 | HTTP 与路径 | 参数或请求体 | 返回 `.data` | 验证范围 |
| --- | --- | --- | --- | --- |
| `auth.user_info()` | GET `/api/auth/user-info` | 可选 `sessionId` | `UserInfo` | SESSION 与匿名行为已实测；sessionId 分支未测 |
| `auth.service_account_subscribed()` | GET `/api/user/service-account-subscribed` | 无 | `bool` | 已实测 |
| `auth.request_checkin_ticket(code=...)` | POST `/platform-app/api/auth/checkin-ticket`（同源） | `{"code": "uni.login 返回值"}` | `CheckinTicket`，`.ticket` 为 `SecretStr` | 小程序契约；本地 HTTP 验证，未调用线上签发 |
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

`CheckinTicketError` 是 `AuthorizationError` 的子类；其它业务错误为 `APIError`。操作票据业务码优先识别，包括 HTTP 403 中的票据错误；其余 HTTP 401/403 分别映射为认证/授权异常。普通业务请求的其它非成功 HTTP 响应保留 `httpx2.HTTPStatusError`，网络错误保留 `httpx2` 原始异常；重定向、登录 HTML 和不匹配的响应契约为 `ProtocolError`。显式 CAS 换会话的方法采用上节单独说明的跳转和错误规则。SDK 不重试，不吞掉错误，也不在错误消息中附带请求 Cookie、票据或完整响应正文。

## 逆向依据与验证

Chrome 获取的学生前端入口：

- [主 bundle](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/index-BsTuEIoc.js)：请求拦截、SESSION 入口、角色菜单、操作票据保存和错误处理。
- [学生 API 模块](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/student.KytIoD5T.js)：8 个本人接口、4 个班委接口。
- [学生今日页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-report-today-home-index.pcAPAkGF.js)：打卡、填报、定位诊断请求体及逆地址接口。
- [历史记录页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-historical-check-in-home-index.BHcOibFM.js)：年月筛选和 clockIn 三态。
- [班委页](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/pages-student-class-check-in-home-index.HSDyIZfu.js)：班级查询、计数、批量情况上报。
- [公共模块](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/index.DNnsTcL3.js)、[头像组件](https://jzsz.uestc.edu.cn/daily-checkin-h5/assets/Avatar.D0sun_0x.js)：情况关键字、附加字典、FastDFS 图片地址。
- 小程序 `reversed/modules/utils/checkinTicket.js:67–114`、`utils/http.js:62–100`、`interceptors/request.js:50–55`（调用方提供的离线脚本）：微信 code → 平台签发、原始 `ok` 判断、票据格式及 SESSION 请求头。

已用 Python SDK 对真实服务执行表中标记的只读路径，并确认匿名请求失败。没有打卡、填报、定位日志或班委业务写入；没有测试管理权限或他人数据。凭据和真实个人响应未写入仓库。

本地检查：

```sh
uv run pyright
uv run pyright --verifytypes moon_uestc --ignoreexternal
uv run python -m unittest discover -s tests -v
```

此前已有 22 个 Python 回归测试通过：14 项覆盖业务接口的认证/授权、票据前置条件、不重试提交、重定向凭据隔离、响应模型、分页与请求边界；8 项覆盖 CAS 换会话、SSO 轮换、原客户端不变、凭据隔离、二次认证、非法跳转、平台状态与应用身份校验。

此前逆向函数已用有效 SSO Cookie 换出新 SESSION 并核对同一学生身份；CAS 换会话 SDK 方法另用失效凭据验证了 `ReauthenticationRequiredError`，成功流程经本地 HTTP 服务验证。本次没有向真实服务提交账号密码或业务写入。

另用本地 HTTP 服务运行了打卡、填报、诊断日志协议，验证 Cookie、票据、序列化、无 data 成功响应及 HTTP 403 错误映射。`uv build` 可生成 wheel 和源码包，wheel 包含类型标记。真实打卡授权仍需官方小程序有效票据；这里的写操作验证不代表线上提交成功。

Go SDK 另通过 `gofmt`、`go vet`、24 个本地测试和 `go test -race`，并针对真实服务验证了匿名 SESSION 缺失会返回 `AuthenticationError(code=5000)`。Go 客户端没有用真实 SESSION 重跑已认证读取，也没有线上提交写入；这部分沿用 Python 的线上只读结果和本地 HTTP 写入协议验证。Go 测试命令见 [go-sdk/README.md](go-sdk/README.md)。

2026-10-23 补充 IDAS 密码认证 SDK，依据 Chrome 中的 [common-header.js](https://idas.uestc.edu.cn/authserver/uestcTheme/static/common/common-header.js?v=20260910.204435)、[login.js](https://idas.uestc.edu.cn/authserver/uestcTheme/static/web/js/login.js?v=20260910.204435)、[encrypt.js](https://idas.uestc.edu.cn/authserver/uestcTheme/static/common/encrypt.js?v=20260910.204435) 及滑块脚本。真实匿名 HTTP 调用已验证登录页解析、新 SDK 设备标识注册、滑块图片与附带密钥、注销。Python 加密输出与部署的 CryptoJS 在固定测试随机源下逐字节一致。本地真实 HTTP 场景验证密码/滑块签名、CASTGC → SESSION → 学生接口及注销；这不等于线上密码登录成功。未提交真实凭据、未自动解验证码、未验证 MFA 后续提交。

IDAS SDK 阶段已有 36 个回归测试通过，新增 14 项覆盖设备标识复用、Cookie 签发/作用域/冲突、密码加密、隐藏字段保留、验证码类型与会话连续性、错误密码与旧票据隔离、二次认证、非法跳转、注销及浏览器指纹计算。严格类型检查通过，公开类型完整性为 100%；`uv build` 已生成包含认证模块的 wheel 和源码包。

命令行人工验证码已用真实 PTY 和本地 HTTP 服务验证：图片输入、滑块按键移动、验证码校验后继续密码登录、仅一次密码提交，以及 `0600` 凭据文件。另从真实站点取出匿名滑块题，在终端显示后取消，没有提交账号密码或验证码答案。终端会在退出、取消和异常时恢复；MFA 仍按上节说明处理。

命令行阶段已有 58 个 Python 回归测试通过，包含终端输入、换题、取消、无移动不提交、服务端拒绝、同会话验证码续交、密码不自动重试，以及凭据文件权限与防覆盖。该阶段严格类型检查无错误，公开类型完整性为 100%；安装后的 `moon-uestc login --help` 与 `uv build` 均已运行。

2026-10-23 增加显式微信 code 换操作票据。Python 当前 62 个回归测试通过，严格类型检查无错误，公开类型完整性为 100%；Go 通过 `gofmt`、`go vet ./...` 和 `go test -race ./... -count=1 -v`。两个 SDK 均以本地真实 HTTP 服务验证取票 → 显式设置票据 → 打卡/填报协议，包括 SESSION、`mp-weixin`/`h5` 请求隔离、自定义业务前缀、只有 `ok: true` 的成功响应、`ok: false` 不接受 `code: 0`、失败不替换旧票据及不重试。没有调用线上签发、打卡或填报接口；完整微信取 code 流程因缺少目标小程序运行环境未实现、未验证。

`main.py` 的短信二次认证依据部署的 [reAuth.js](https://idas.uestc.edu.cn/authserver/uestcTheme/static/custom/js/reAuth.js?v=20260910.204435) 实现。使用已有 CAS 凭据实测确认了业务级二次认证页、短信方式可用及服务端切换响应；新脚本已在线运行至发送验证码前的确认提示，随后取消，没有发送真实短信或提交真实验证码。完整密码登录 → 短信认证 → 平台会话校验 → 缓存复用已用真实 PTY 和模拟 HTTP transport 验证，另有 6 项回归覆盖 Cookie 轮换、拒绝验证码、不自动信任设备、认证方式限制和目标业务校验；这不等于真实短信验证已通过。
