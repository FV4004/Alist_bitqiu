package bitqiu

import (
	"errors"
	"net/url"
	"strconv"
	"time"
)

// cloudDownloadForm reconstructs the native sign bridge at 0x140020c20.
// It performs no I/O and never initializes, refreshes, or persists a session.
// No cloud-download tool is registered until the remaining protocol gaps in
// docs/BITQIU_CLOUD_DOWNLOAD_PROTOCOL.md have been resolved.
func (d *BitQiu) cloudDownloadForm(fields url.Values, now time.Time) (url.Values, error) {
	d.clientMu.Lock()
	defer d.clientMu.Unlock()
	if !d.windowsMode() || d.state.Binding != d.binding() || d.state.Token == "" || d.state.OpenID == "" || d.state.UserID == "" || d.ClientPlatform == "" {
		return nil, errors.New("BitQiu 云下载需要现有的有效 Windows 会话和设备标识")
	}
	form := url.Values{}
	for key, values := range fields {
		switch key {
		case "access_token", "open_id", "user_id", "platform", "app_id", "timestamp", "sign":
			return nil, errors.New("禁止覆盖 BitQiu 云下载会话或签名字段")
		}
		if key == "" || len(values) != 1 {
			return nil, errors.New("BitQiu 云下载签名仅接受单值字段")
		}
		form.Set(key, values[0])
	}
	form.Set("access_token", d.state.Token)
	form.Set("open_id", d.state.OpenID)
	form.Set("user_id", d.state.UserID)
	form.Set("app_id", clientAppID)
	form.Set("platform", d.ClientPlatform)
	form.Set("timestamp", strconv.FormatInt(now.UnixMilli(), 10))
	// The native bridge excludes empty caller values from the signing map,
	// while retaining them in the returned form. It does not inject org_channel.
	signed := url.Values{}
	for key, values := range form {
		if values[0] != "" {
			signed.Set(key, values[0])
		}
	}
	form.Set("sign", signClientForm(signed))
	return form, nil
}
