package oauthapp

import (
	"testing"
)

// The consent page as the hub renders it: hidden fields, an existing key's
// radio, group checkboxes, the key name, and Deny/Allow buttons.
const consentPage = `<form method="post" action="/oauth/authorize">
<input type="hidden" name="client_id" value="oc_1">
<input type="hidden" name="redirect_uri" value="http://127.0.0.1:9/callback">
<input type="hidden" name="csrf" value="c&amp;1">
<label class="group"><input type="radio" name="key" value="k_1" checked><span>app</span></label>
<label class="group"><input type="radio" name="key" value="new"><span>A new key</span></label>
<label class="group"><input type="checkbox" name="group" value="g_all" checked><span>Everything</span></label>
<label class="group"><input type="checkbox" name="group" value="g_ro"><span>Read only</span></label>
<input type="text" id="key_name" name="key_name" value="My app" maxlength="64" autocomplete="off">
<button type="submit" name="action" value="deny">Deny</button>
<button type="submit" name="action" value="allow" class="primary">Allow</button>
</form>`

func TestParseForm(t *testing.T) {
	f, err := parseForm(consentPage)
	if err != nil {
		t.Fatal(err)
	}
	if f.action != "/oauth/authorize" {
		t.Errorf("action = %q", f.action)
	}
	want := map[string][]string{
		"client_id":    {"oc_1"},
		"redirect_uri": {"http://127.0.0.1:9/callback"},
		"csrf":         {"c&1"},
		"key":          {"k_1"},
		"group":        {"g_all"},
		"key_name":     {"My app"},
	}
	for k, v := range want {
		if got := f.values[k]; len(got) != len(v) || got[0] != v[0] {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if f.values.Has("action") {
		t.Errorf("buttons must not be submitted by default: action = %q", f.values["action"])
	}
}

func TestResult(t *testing.T) {
	r, err := result(RedirectURL + "?code=c1&state=s1&iss=https%3A%2F%2Fhub")
	if err != nil || r.Code != "c1" || r.State != "s1" || r.Iss != "https://hub" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if _, err := result(RedirectURL + "?error=access_denied&error_description=no"); err == nil {
		t.Fatal("an error redirect must fail")
	}
}
