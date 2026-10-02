package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

type fakeMailer struct {
	enabled bool
	err     error
	sent    []dto.Mail
}

func (f *fakeMailer) Enabled() bool { return f.enabled }
func (f *fakeMailer) Send(_ context.Context, m dto.Mail) error {
	f.sent = append(f.sent, m)
	return f.err
}

func TestSend(t *testing.T) {
	ok := dto.Mail{To: []string{"a@x.com"}, Subject: "s", Text: "t"}

	if err := Send(context.Background(), &fakeMailer{enabled: false}, ok); !errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("disabled mailer: err = %v, want permanent", err)
	}
	if err := Send(context.Background(), &fakeMailer{enabled: true}, dto.Mail{Subject: "s", Text: "t"}); !errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("invalid mail: err = %v, want permanent", err)
	}
	transient := &fakeMailer{enabled: true, err: errors.New("smtp 421")}
	if err := Send(context.Background(), transient, ok); err == nil || errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("transport error: err = %v, want retryable", err)
	}
	good := &fakeMailer{enabled: true}
	if err := Send(context.Background(), good, ok); err != nil || len(good.sent) != 1 {
		t.Errorf("good send: err=%v sent=%d", err, len(good.sent))
	}
}
