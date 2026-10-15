package bitqiu

import (
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
)

type Addition struct {
	driver.RootID
	Username       string `json:"username" required:"true"`
	Password       string `json:"password" required:"true"`
	LoginMode      string `json:"login_mode" type:"select" options:"windows,web" default:"windows" help:"windows: 客户端密码登录、设备短信验证及原文件下载；web: 原网页驱动。"`
	LoginAction    string `json:"login_action" type:"select" options:"none,send_sms,verify_sms,reset_login,diagnose_http" default:"none" help:"新建时保持 none；Driver 启动遇到新设备验证会自动发送首条短信。收到后填写 sms_code 保存即可；send_sms 仅用于重发，reset_login 重新登录；diagnose_http 对比根目录及首个子目录的已登录请求，仅在存储状态输出脱敏结果，不重新登录或发送短信。"`
	SMSCode        string `json:"sms_code" type:"string" required:"false" help:"新建存储时留空。Driver 启动发送短信后，重新打开编辑页填写并保存；仅提交一次，随后自动清空。"`
	ClientPlatform string `json:"client_platform" help:"客户端设备标识，留空自动生成并持久保存。验证过程中不要更改。"`
	ClientChannel  string `json:"client_channel" default:"pc" help:"Windows 客户端渠道，默认 pc。"`
	ClientState    string `json:"client_state" type:"text" help:"自动保存的登录/验证状态，含敏感凭据，不要分享或编辑。更换账号会自动失效。"`
	UserPlatform   string `json:"user_platform" help:"Optional device identifier; auto-generated if empty."`
	OrderType      string `json:"order_type" type:"select" options:"updateTime,createTime,name,size" default:"updateTime"`
	OrderDesc      bool   `json:"order_desc"`
	PageSize       string `json:"page_size" default:"24" help:"Number of entries to request per page."`
	UserAgent      string `json:"user_agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"`
}

var config = driver.Config{
	Name:        "BitQiu",
	DefaultRoot: "0",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &BitQiu{}
	})
}
