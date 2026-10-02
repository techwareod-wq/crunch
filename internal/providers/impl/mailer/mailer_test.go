package mailer

import (
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/dto"
)

func TestBuildMessage_Multipart(t *testing.T) {
	raw, err := BuildMessage("WarehouseHub <no-reply@x.com>", dto.Mail{
		To:      []string{"sales@x.com", "ops@x.com"},
		ReplyTo: "visitor@y.com",
		Subject: "New enquiry — Bhiwandi",
		Text:    "hello",
		HTML:    "<p>hello</p>",
	}, "BND")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"To: sales@x.com, ops@x.com\r\n",
		"Reply-To: visitor@y.com\r\n",
		"Subject: =?utf-8?q?",
		`Content-Type: multipart/alternative; boundary="BND"`,
		"--BND\r\nContent-Type: text/plain; charset=UTF-8",
		"--BND\r\nContent-Type: text/html; charset=UTF-8",
		"--BND--\r\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q:\n%s", want, s)
		}
	}
}

func TestBuildMessage_PlainOnly(t *testing.T) {
	raw, _ := BuildMessage("a@x.com", dto.Mail{To: []string{"b@x.com"}, Subject: "s", Text: "t"}, "BND")
	s := string(raw)
	if strings.Contains(s, "multipart") || strings.Contains(s, "Reply-To") || !strings.Contains(s, "Content-Type: text/plain") {
		t.Errorf("unexpected plain message:\n%s", s)
	}
}

func TestValidate(t *testing.T) {
	good := dto.Mail{To: []string{"a@x.com"}, Subject: "s", Text: "t"}
	if err := Validate(good); err != nil {
		t.Fatalf("good mail rejected: %v", err)
	}
	bad := []dto.Mail{
		{Subject: "s", Text: "t"},
		{To: []string{"not-an-address"}, Subject: "s", Text: "t"},
		{To: []string{"a@x.com"}, Subject: "s\r\nBcc: evil@x.com", Text: "t"},
		{To: []string{"a@x.com"}, ReplyTo: "x\r\n@y", Subject: "s", Text: "t"},
		{To: []string{"a@x.com"}, Subject: "s"},
	}
	for i, m := range bad {
		if err := Validate(m); err == nil {
			t.Errorf("bad[%d] accepted", i)
		}
	}
}
