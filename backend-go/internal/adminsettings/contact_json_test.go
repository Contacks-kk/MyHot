package adminsettings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContactJSONUsesPackDefaultsPreservesExplicitNullAndUnknownFields(t *testing.T) {
	wechatDefault := "/contact/qr-wechat-pack.png"
	feishuDefault := "/contact/qr-feishu-pack.png"
	contact := Contact{WechatQR: &wechatDefault, FeishuQR: &feishuDefault}
	if err := json.Unmarshal([]byte(`{"wechatQr":null,"extraContact":"kept"}`), &contact); err != nil {
		t.Fatal(err)
	}
	if contact.WechatQR != nil || contact.FeishuQR == nil || *contact.FeishuQR != feishuDefault {
		t.Fatalf("decoded contact = %#v", contact)
	}
	encoded, err := json.Marshal(contact)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"wechatQr":null`, `"feishuQr":"/contact/qr-feishu-pack.png"`, `"extraContact":"kept"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("contact JSON %s is missing %s", encoded, want)
		}
	}
}
