package format

import "testing"

func TestSharedChildPasswordCrossLanguageVector(t *testing.T) {
	uuid := "11111111-1111-4111-8111-111111111111"
	want := "f25610061245bf61167e66ad0bb797cc0c689fa70490439ce3439947e272c2a3"
	if got := SharedChildPassword(uuid, 26); got != want {
		t.Fatalf("got %q", got)
	}
	if SharedChildPassword(uuid, 27) == want || SharedChildPassword("other-user", 26) == want {
		t.Fatal("different accounts and children must have different credentials")
	}
}

func TestSharedChildIdentityRoundTrip(t *testing.T) {
	main := UserTag("parent-25", "user-uuid")
	alias := SharedChildUserTag("parent-25", "user-uuid", 26)
	if canonical, id := SplitSharedChildUser(alias); canonical != main || id != 26 {
		t.Fatalf("wrong identity: %q %d", canonical, id)
	}
	for _, email := range []string{main, main + "|child:0", main + "|child:-1", main + "|child:026", main + "|child:26x"} {
		if canonical, id := SplitSharedChildUser(email); canonical != email || id != 0 {
			t.Fatalf("ordinary or malformed identity changed: %q", email)
		}
	}
}
