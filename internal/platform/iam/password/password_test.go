package password

import "testing"

func TestHashVerify(t *testing.T) {
	h, err := Hash("Club#Secret2026")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := Verify("Club#Secret2026", h); !ok {
		t.Fatal("verify failed")
	}
	if ok, _ := Verify("wrong", h); ok {
		t.Fatal("wrong password verified")
	}
}

func TestPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{"short1A": false, "alllowercase1": false, "NoDigitsHere": false, "Good#Pass2026": true, "Dian#2026Secure": false} {
		err := CheckPolicy(pw, "dian@example.com")
		if (err == nil) != ok {
			t.Errorf("%q: got err=%v", pw, err)
		}
	}
	if CheckPIN("111111") == nil || CheckPIN("12a456") == nil || CheckPIN("246810") != nil {
		t.Fatal("PIN policy")
	}
}
