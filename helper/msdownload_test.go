package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const fakeMSPage = `<select id="product-edition"><option value="" selected>Select Download</option>
<option value="3321">Windows 11 (multi-edition ISO for x64 devices)</option></select>
<table><tr><td>Arabic 64-bit</td><td>10BF2F86F148D6CF06BDFFE3010C413187C72516A49EB61B3643977EFFC52D14</td></tr>
<tr><td>Brazilian Portuguese 64-bit</td><td>50FE4703CF0DF0072E093D1F5D58ED450E4C49D8CA960433BBE6278D5EF10107</td></tr>
<tr><td>Chinese Simplified 64-bit</td><td>7408581E67BC455EBAAFB9230E531ABF45B1C8864A22114A1B03893F897102E4</td></tr></table>`

func TestParseMSPage(t *testing.T) {
	id, h := parseMSPage(fakeMSPage, "x64")
	if id != "3321" {
		t.Fatalf("edição %q", id)
	}
	if h[normLang("Brazilian Portuguese 64-bit")] != strings.ToLower("50FE4703CF0DF0072E093D1F5D58ED450E4C49D8CA960433BBE6278D5EF10107") {
		t.Fatalf("hash pt-BR não achado: %v", h)
	}
	if h[normLang("Chinese (Simplified) 64-bit")] == "" {
		t.Fatal("normalização de parênteses falhou")
	}
}

func fakeMS(t *testing.T, blocked bool) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/en-us/software-download/windows11":
			fmt.Fprint(w, fakeMSPage)
		case strings.HasSuffix(r.URL.Path, "/getskuinformationbyproductedition"):
			if r.URL.Query().Get("ProductEditionId") != "3321" || r.URL.Query().Get("sessionID") == "" {
				http.Error(w, "bad", 400)
				return
			}
			fmt.Fprint(w, `{"ValidationContainer":{"Errors":[]},"Skus":[{"Id":"20035","Language":"Arabic","LocalizedLanguage":"Arabic"},{"Id":"20036","Language":"Brazilian Portuguese","LocalizedLanguage":"Brazilian Portuguese"}]}`)
		case strings.HasSuffix(r.URL.Path, "/GetProductDownloadLinksBySku"):
			if blocked {
				fmt.Fprint(w, `{"Errors":[{"Key":"ErrorSettings.SentinelReject","Value":"Sentinel marked this request as rejected.","Type":8}]}`)
				return
			}
			if r.URL.Query().Get("SKU") != "20036" || r.Header.Get("Referer") == "" {
				http.Error(w, "bad", 400)
				return
			}
			fmt.Fprintf(w, `{"ProductDownloadOptions":[{"Uri":"%s/dl/Win11_25H2_BrazilianPortuguese_x64_v2.iso?t=abc","DownloadType":1}],"DownloadExpirationDatetime":"2030-01-01T00:00:00Z"}`, srv.URL)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			w.Header().Set("Content-Range", "bytes 0-0/8172068864")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte{0})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestMSGetLink(t *testing.T) {
	srv := fakeMS(t, false)
	defer srv.Close()
	old := msBase
	msBase = srv.URL
	defer func() { msBase = old }()
	l, err := msGetLink(context.Background(), "pt-BR", "x64")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Win11_25H2_BrazilianPortuguese_x64_v2.iso" || l.Size != 8172068864 || !strings.HasPrefix(l.SHA256, "50fe47") {
		t.Fatalf("link inesperado: %+v", l)
	}
	if _, err := msGetLink(context.Background(), "xx-YY", "x64"); err == nil {
		t.Fatal("idioma desconhecido deveria falhar")
	}
	srv2 := fakeMS(t, true)
	defer srv2.Close()
	msBase = srv2.URL
	if _, err := msGetLink(context.Background(), "pt-BR", "x64"); !errors.Is(err, errMSBlocked) {
		t.Fatalf("bloqueio da Microsoft não reconhecido: %v", err)
	}
}

// Teste real (opcional, UAB_MS_LIVE=1): pede o link à Microsoft e baixa só os primeiros 4 MB.
func TestMSLive(t *testing.T) {
	if os.Getenv("UAB_MS_LIVE") != "1" {
		t.Skip("UAB_MS_LIVE != 1")
	}
	l, err := msGetLink(context.Background(), "pt-BR", "x64")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("arquivo %s, %d bytes, sha256 publicado %s", l.Name, l.Size, l.SHA256)
	req, _ := http.NewRequest(http.MethodGet, l.URL, nil)
	req.Header.Set("Range", "bytes=0-4194303")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || len(b) != 4<<20 {
		t.Fatalf("Range falhou: %s %d", resp.Status, len(b))
	}
	if string(b[0x8001:0x8006]) != "CD001" || string(b[20*2048+1:20*2048+6]) != "NSR02" {
		t.Fatal("começo do arquivo não parece ISO UDF")
	}
	t.Logf("Range OK (%s): ISO9660 + UDF (NSR02) no começo", resp.Header.Get("Content-Range"))
}
