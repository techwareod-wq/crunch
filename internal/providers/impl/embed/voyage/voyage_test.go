package voyage

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeClient struct {
	gotURL  string
	gotBody any
	gotHdr  map[string]string
	status  int
	body    string
}

func (f *fakeClient) Get(context.Context, string, map[string]string) ([]byte, int, error) {
	return nil, 0, nil
}
func (f *fakeClient) Put(context.Context, string, any, map[string]string) ([]byte, int, error) {
	return nil, 0, nil
}
func (f *fakeClient) Delete(context.Context, string, map[string]string) ([]byte, int, error) {
	return nil, 0, nil
}
func (f *fakeClient) Post(_ context.Context, url string, body any, h map[string]string) ([]byte, int, error) {
	f.gotURL, f.gotBody, f.gotHdr = url, body, h
	return []byte(f.body), f.status, nil
}

func TestEmbed(t *testing.T) {
	fc := &fakeClient{status: 200, body: `{"data":[{"embedding":[0.3],"index":1},{"embedding":[0.1,0.2],"index":0}]}`}
	v := New(fc, "https://api.voyageai.com/v1/embeddings", "key", "voyage-3.5-lite", 1024, 0)
	out, err := v.Embed(context.Background(), []string{"a", "b"}, "document")
	if err != nil || len(out) != 2 || out[0][1] != 0.2 || out[1][0] != 0.3 {
		t.Fatalf("out = %v err = %v", out, err)
	}
	b, _ := json.Marshal(fc.gotBody)
	if string(b) != `{"input":["a","b"],"model":"voyage-3.5-lite","input_type":"document","output_dimension":1024}` || fc.gotHdr["Authorization"] != "Bearer key" {
		t.Errorf("request = %s %v", b, fc.gotHdr)
	}
	fc.status, fc.body = 401, `{"detail":"bad key"}`
	if _, err := v.Embed(context.Background(), []string{"a"}, "query"); err == nil || err.Error() != "voyage: http 401: bad key" {
		t.Errorf("err = %v", err)
	}
}
