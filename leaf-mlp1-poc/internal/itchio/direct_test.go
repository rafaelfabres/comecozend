package itchio

import (
	"net/url"
	"strings"
	"testing"
)

// A free page with per-file download buttons (no purchase step), as on
// jwgllc.itch.io/slender-the-8-gb-pages.
const directPage = `<html><body>
<div class="upload_list_widget">
 <div class="upload">
  <div class="upload_name"><strong title="SLENDER THE 8 GBC PAGES .gb" class="name">SLENDER THE 8 GBC PAGES .gb</strong></div>
  <a data-upload_id="9876543" class="button download_btn" href="javascript:void(0)">Download</a>
  <div class="upload_date"><span class="version_name">Version 1.0.7</span></div>
 </div>
</div></body></html>`

func TestDirectUploads(t *testing.T) {
	ups := directUploads("https://jwgllc.itch.io/slender-the-8-gb-pages", "TOKEN", []byte(directPage))
	if len(ups) != 1 || ups[0].UploadID != "9876543" || ups[0].Version != "1.0.7" {
		t.Fatalf("uploads: %+v", ups)
	}
	u, _ := url.Parse(ups[0].URL)
	if u.Query().Get("key") != "" || u.Query().Get("csrf") != "TOKEN" || !strings.HasSuffix(u.Path, "/file/9876543") {
		t.Errorf("resolver URL: %s", ups[0].URL)
	}
}
