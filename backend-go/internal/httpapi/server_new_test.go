package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledContactDefaultsMatchSortedPackImages(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"qr-wechat-z.png", "qr-wechat-a.jpg", "qr-feishu-b.webp", "qr-feishu-c.gif"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "qr-wechat-00.png"), 0o700); err != nil {
		t.Fatal(err)
	}

	defaults := bundledContactDefaults(directory)
	if defaults.WechatQR == nil || *defaults.WechatQR != "/contact/qr-wechat-a.jpg" {
		t.Errorf("wechat pack default = %v", defaults.WechatQR)
	}
	if defaults.FeishuQR == nil || *defaults.FeishuQR != "/contact/qr-feishu-b.webp" {
		t.Errorf("feishu pack default = %v", defaults.FeishuQR)
	}
}
