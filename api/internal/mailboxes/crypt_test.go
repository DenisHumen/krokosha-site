package mailboxes

import (
	"os/exec"
	"strings"
	"testing"
)

// The test vectors of the specification (https://www.akkadia.org/drepper/SHA-crypt.txt).
func TestSpecificationVectors(t *testing.T) {
	for _, tc := range []struct{ setting, password, want string }{
		{"$6$saltstring", "Hello world!",
			"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"$6$rounds=10000$saltstringsaltstring", "Hello world!",
			"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
		{"$6$rounds=5000$toolongsaltstring", "This is just a test",
			"$6$rounds=5000$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0"},
		{"$6$rounds=1400$anotherlongsaltstring", "a very much longer text to encrypt.  This one even stretches over morethan one line.",
			"$6$rounds=1400$anotherlongsalts$POfYwTEok97VWcjxIiSOjiykti.o/pQs.wPvMxQ6Fm7I6IoYN3CmLs66x9t0oSwbtEW7o7UmJEiDwGqd8p4ur1"},
		{"$6$rounds=77777$short", "we have a short salt string but not a short password",
			"$6$rounds=77777$short$WuQyW2YR.hBNpjjRhpYD/ifIw05xdfeEyQoMxIXbkvr0gge1a1x3yRULJ5CCaUeOxFmtlcGZelFl5CxtgfiAc0"},
		{"$6$rounds=123456$asaltof16chars..", "a short string",
			"$6$rounds=123456$asaltof16chars..$BtCwjqMJGx5hrJhZywWvt0RLE8uZ4oPwcelCjmw2kSYu.Ec6ycULevoBK25fs2xXgMNrCzIMVcgEJAstJeonj1"},
		{"$6$rounds=10$roundstoolow", "the minimum number is still observed",
			"$6$rounds=1000$roundstoolow$kUMsbe306n21p9R.FRkW3IGn.S9NPN0x50YhH1xhLsPuWGsUSklZt58jaTfF4ZEQpyUNGc0dqbpBYYBaHHrsX."},
	} {
		got, err := CryptSetting(tc.password, tc.setting)
		if err != nil || got != tc.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", tc.setting, got, err, tc.want)
		}
	}
}

func TestCryptAndVerify(t *testing.T) {
	hash, err := Crypt("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$6$rounds=100000$") {
		t.Fatalf("hash %q is not what the helper accepts", hash)
	}
	if !Verify("correct horse battery staple", hash) || Verify("correct horse battery stapler", hash) {
		t.Error("Verify")
	}
	other, _ := Crypt("correct horse battery staple")
	if other == hash {
		t.Error("two hashes of one password are the same: the salt is not random")
	}
}

// The same result as the tool the helper script uses, when the machine has it.
func TestSameAsOpenSSL(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("no openssl here")
	}
	out, err := exec.Command(openssl, "passwd", "-6", "-salt", "Kr0k0shaSalt", "пароль с пробелами и кириллицей").Output()
	if err != nil {
		t.Skip("openssl passwd -6 is not supported:", err)
	}
	want := strings.TrimSpace(string(out))
	if got, _ := CryptSetting("пароль с пробелами и кириллицей", "$6$Kr0k0shaSalt"); got != want {
		t.Errorf("got %s, openssl %s", got, want)
	}
}
