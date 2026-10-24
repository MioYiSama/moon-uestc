package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/MioYiSama/moonuestc/internal/app"
)

type pageData struct {
	Template     string
	Title        string
	Nav          string
	CSRF         string
	Error        string
	ErrorTitle   string
	Accounts     []accountRow
	Account      accountRow
	Runs         []runRow
	AccountCount int
	RunCount     int
	NextCheckin  string
	Coordinates  string
	Scan         scanPageModel
}

type accountRow struct {
	ID                    int64
	Name                  string
	Username              string
	SchoolUserID          string
	CredentialStatus      string
	CredentialTone        string
	CredentialExplanation string
	CredentialIssue       string
	BusinessStatus        string
	BusinessTone          string
	BusinessExplanation   string
	BusinessCheckedAt     string
	BusinessError         string
	LastRefresh           string
	Expires               string
	TodayStatus           string
	DeleteName            string
	DeleteConfirmValue    string
}

type runRow struct {
	AccountID    int64
	Name         string
	Username     string
	BusinessDate string
	Status       string
	StatusTone   string
	Stage        string
	ErrorCode    string
	Message      string
	StartedAt    string
	FinishedAt   string
}

type scanPageModel struct {
	ID          string
	Status      string
	StatusLabel string
	Message     string
	TargetID    int64
	TargetName  string
	AccountID   int64
	Terminal    bool
}

func makeAccountRow(view app.AccountView) accountRow {
	name := view.SchoolName
	if name == "" {
		name = "未命名学校账号"
	}
	username := visibleOrDash(view.SchoolUsername)
	studentID := visibleOrDash(view.SchoolUserID)
	return accountRow{
		ID:                    view.ID,
		Name:                  name,
		Username:              username,
		SchoolUserID:          studentID,
		CredentialStatus:      credentialLabel(view.CredentialStatus),
		CredentialTone:        credentialTone(view.CredentialStatus),
		CredentialExplanation: credentialExplanation(view.CredentialStatus),
		CredentialIssue:       credentialIssue(view),
		BusinessStatus:        businessLabel(view.BusinessStatus),
		BusinessTone:          businessTone(view.BusinessStatus),
		BusinessExplanation:   businessExplanation(view.BusinessStatus),
		BusinessCheckedAt:     timestamp(view.LastBusinessCheckAt),
		BusinessError:         safeCode(view.BusinessErrorCode),
		LastRefresh:           timestamp(view.LastRefreshAt),
		Expires:               timestampValue(view.ExpiresAt),
		TodayStatus:           todayLabel(view.TodayStatus),
		DeleteName:            deleteName(name, view.SchoolUsername, view.SchoolUserID),
		DeleteConfirmValue:    strconv.FormatInt(view.ID, 10),
	}
}

func makeRunRow(view app.RunView) runRow {
	name := view.SchoolName
	if name == "" {
		name = "学校账号"
	}
	return runRow{
		AccountID:    view.AccountID,
		Name:         name,
		Username:     visibleOrDash(view.SchoolUsername),
		BusinessDate: visibleOrDash(view.BusinessDate),
		Status:       runStatusLabel(view.Status),
		StatusTone:   runStatusTone(view.Status),
		Stage:        stageLabel(view.Stage),
		ErrorCode:    safeCode(view.ErrorCode),
		Message:      view.Message,
		StartedAt:    timestampValue(view.StartedAt),
		FinishedAt:   timestamp(view.FinishedAt),
	}
}

func timestamp(value *int64) string {
	if value == nil {
		return "—"
	}
	return timestampValue(*value)
}

func timestampValue(value int64) string {
	if value <= 0 {
		return "—"
	}
	return time.Unix(value, 0).In(beijing).Format("2006-01-02 15:04")
}

func visibleOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func deleteName(name, username, studentID string) string {
	identity := username
	if identity == "" {
		identity = studentID
	}
	if identity == "" {
		return name
	}
	return name + "（" + identity + "）"
}

func credentialLabel(status string) string {
	switch status {
	case "healthy":
		return "保活正常"
	case "error":
		return "保活暂时失败"
	case "needs_rescan":
		return "需要重新扫码"
	default:
		return "状态未知"
	}
}

func credentialTone(status string) string {
	switch status {
	case "healthy":
		return "positive"
	case "error":
		return "warning"
	case "needs_rescan":
		return "negative"
	default:
		return "neutral"
	}
}

func credentialExplanation(status string) string {
	switch status {
	case "healthy":
		return "凭据可用，系统按到期时间保活。"
	case "error":
		return "最近一次保活失败，已保留原凭据并安排再次检查。"
	case "needs_rescan":
		return "自动保活已停止，请重新扫码授权。"
	default:
		return "凭据状态暂不可识别。"
	}
}
func credentialIssue(view app.AccountView) string {
	if view.CredentialStatus != "error" {
		return ""
	}
	stage := stageLabel(view.LastErrorStage)
	code := safeCode(view.LastErrorCode)
	switch {
	case stage != "—" && code != "":
		return "最近错误：" + stage + " · 错误码 " + code
	case stage != "—":
		return "最近错误：" + stage
	case code != "":
		return "最近错误码 " + code
	default:
		return ""
	}
}

func businessLabel(status string) string {
	switch status {
	case "ready":
		return "可访问签到业务"
	case "rejected":
		return "业务会话未通过"
	case "unchecked":
		return "尚未校验"
	default:
		return "状态未知"
	}
}

func businessTone(status string) string {
	switch status {
	case "ready":
		return "positive"
	case "rejected":
		return "negative"
	case "unchecked":
		return "neutral"
	default:
		return "neutral"
	}
}

func businessExplanation(status string) string {
	switch status {
	case "ready":
		return "微信来源的签到业务会话已校验通过。"
	case "rejected":
		return "微信已绑定，签到业务会话未通过校验。"
	case "unchecked":
		return "尚未完成微信来源的签到业务会话校验。"
	default:
		return "业务会话状态暂不可识别。"
	}
}

func todayLabel(status string) string {
	switch status {
	case "running", "success", "already_done", "skipped", "failed", "unknown":
		return runStatusLabel(status)
	}
	switch strings.ToUpper(status) {
	case "CHECKED_IN":
		return "已签到"
	case "STUDENT_REPORTED":
		return "已由学生填报"
	case "INSTRUCTOR_REPORTED":
		return "已由教师填报"
	case "EXEMPT":
		return "已豁免"
	case "PENDING":
		return "待签到"
	case "":
		return "—"
	default:
		return "状态待确认"
	}
}

func runStatusLabel(status string) string {
	switch status {
	case "running":
		return "执行中"
	case "success":
		return "成功"
	case "already_done":
		return "已签到"
	case "skipped":
		return "已跳过"
	case "failed":
		return "失败"
	case "unknown":
		return "结果待确认"
	default:
		return "状态未知"
	}
}

func runStatusTone(status string) string {
	switch status {
	case "success", "already_done":
		return "positive"
	case "failed":
		return "negative"
	case "unknown":
		return "warning"
	default:
		return "neutral"
	}
}

func stageLabel(stage string) string {
	switch stage {
	case "credential", "refresh", "credential_refresh":
		return "微信凭据保活"
	case "code":
		return "获取微信授权码"
	case "check_bind":
		return "小程序绑定校验"
	case "mini_user_info", "mini_identity":
		return "微信身份校验"
	case "business_session":
		return "签到业务会话"
	case "today":
		return "查询今日签到"
	case "ticket_code", "ticket":
		return "获取签到票据"
	case "reverse", "reverse_geocode":
		return "位置反向解析"
	case "checkin":
		return "提交签到"
	case "":
		return "—"
	default:
		return "未知阶段"
	}
}

func safeCode(code string) string {
	if len(code) == 0 || len(code) > 40 {
		return ""
	}
	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			continue
		}
		return ""
	}
	return code
}

func nextCheckinLabel() string {
	next := app.NextCheckin(time.Now()).In(beijing)
	return next.Format("2006-01-02 15:04") + " 北京时间"
}

func scanTitle(scan scanPageModel) string {
	if scan.TargetID > 0 {
		return "重新扫码"
	}
	return "扫码添加账号"
}
