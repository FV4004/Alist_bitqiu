package bitqiu

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cloudFormDriver() *BitQiu {
	d := &BitQiu{Addition: Addition{Username: "synthetic-user", ClientPlatform: "persisted-device"}}
	d.state = clientState{Binding: d.binding(), Token: "synthetic-token", OpenID: "synthetic-open", UserID: "9007199254740993"}
	return d
}

func TestCloudFormNativeBridgeVector(t *testing.T) {
	d := cloudFormDriver()
	input := url.Values{"page": {"1"}, "limit": {"20"}, "status": {"0"}, "task": {""}}
	before := d.Addition
	form, err := d.cloudDownloadForm(input, time.UnixMilli(1793164800123))
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic vector reconstructed from native 0x140020c20/0x1401d2560,
	// independently calculated with Python hashlib, not a captured response.
	if form.Get("sign") != "7182aff2470254f0f52c02bbc4b82a51" {
		t.Fatal("native bridge signing vector mismatch")
	}
	if form.Get("platform") != "persisted-device" || form.Get("user_id") != "9007199254740993" || form.Get("timestamp") != "1793164800123" {
		t.Fatal("device, precise identity or millisecond timestamp changed")
	}
	if _, ok := form["task"]; !ok {
		t.Fatal("empty caller field was removed from returned form")
	}
	if _, ok := form["org_channel"]; ok {
		t.Fatal("injected field absent from native sign bridge")
	}
	form.Set("page", "2")
	if input.Get("page") != "1" || before != d.Addition || d.client != nil {
		t.Fatal("input, storage or HTTP client was mutated")
	}
}

func TestCloudFormDoesNotLoginOrGenerateDevice(t *testing.T) {
	for _, change := range []func(*BitQiu){
		func(d *BitQiu) { d.LoginMode = "web" },
		func(d *BitQiu) { d.state.Token = "" },
		func(d *BitQiu) { d.state.OpenID = "" },
		func(d *BitQiu) { d.state.UserID = "" },
		func(d *BitQiu) { d.ClientPlatform = ""; d.state.Binding = d.binding() },
		func(d *BitQiu) { d.Username = "changed-account" },
	} {
		d := cloudFormDriver()
		change(d)
		before, state := d.Addition, d.state
		if form, err := d.cloudDownloadForm(nil, time.Now()); err == nil || form != nil {
			t.Fatal("unusable session accepted")
		}
		if before != d.Addition || state != d.state || d.client != nil {
			t.Fatal("invalid session caused state changes or HTTP initialization")
		}
	}
}

func TestCloudFormRejectsAuthOverridesAndAmbiguousArrays(t *testing.T) {
	d := cloudFormDriver()
	for _, key := range []string{"access_token", "open_id", "user_id", "platform", "app_id", "timestamp", "sign", ""} {
		if _, err := d.cloudDownloadForm(url.Values{key: {"do-not-echo"}}, time.Now()); err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("override accepted or field value leaked")
		}
	}
	for _, values := range [][]string{nil, {"first", "second"}} {
		input := url.Values{"download_urls": values}
		if _, err := d.cloudDownloadForm(input, time.Now()); err == nil {
			t.Fatal("ambiguous multi-value encoding accepted")
		}
		if !reflect.DeepEqual(input["download_urls"], values) {
			t.Fatal("input changed")
		}
	}
}
